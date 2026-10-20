// Package service 承载视频域的业务逻辑：管理员上传、视频列表与元信息维护。
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	transcodedto "github.com/sobremesatime/chuangyue/internal/transcode/dto"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/lock"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/mq"
	"github.com/sobremesatime/chuangyue/pkg/semaphore"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/storage"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// coverExts 是封面允许的扩展名。
var coverExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// transcodeSlotKey 是转码队列配额的信号量键，跨副本共享——多副本部署时同一份上限才对得上。
const transcodeSlotKey = "video:transcode:slots"

// transcodeSlotLease 是单次转码配额预约的租期（见 pkg/semaphore 的取值约束）。
//
// 它要盖住「一次上传从预约到登记完成」的最长耗时：单文件是整段落盘，分片是
// 「合并大文件 + 登记 + 投递」，按 upload.max_size_mb（默认 2048）量级留足余量取 30 分钟。
// 反过来也不能更长——它同时是崩溃后泄漏配额的回收期限，最坏情况是少一个可用槽位
// 持续同样长的时间。
const transcodeSlotLease = 30 * time.Minute

// DeletePublisher 把「视频已删除」事件投递给消息队列。
//
// 抽成接口而不是直接依赖 *mq.Producer：Service 只需要「发一条事件」这一件事，
// 单测与未启用 Kafka 的部署都可以不注入（此时删除照常成功，只是不触发互动数据清理）。
type DeletePublisher interface {
	PublishVideoDeleted(ctx context.Context, videoID int64) error
}

// IndexPublisher 把视频变更事件投递给搜索域，用于同步检索索引。
//
// 与 DeletePublisher 同样是可选依赖：未启用 ES 的部署不注入，索引同步自然关闭，
// 搜索服务继续走视频服务的库内检索。
type IndexPublisher interface {
	PublishVideoIndex(ctx context.Context, event dto.VideoIndexEvent) error
}

// Option 是 Service 的可选依赖。
//
// 用可变参数而不是继续加必填参数：New 已有 6 个位置参数，
// 加一个必填项会波及所有测试构造点，而这条依赖本来就是可选的。
type Option func(*Service)

// WithDeletePublisher 注入「视频删除事件」的发布者。
func WithDeletePublisher(p DeletePublisher) Option {
	return func(s *Service) { s.deletePub = p }
}

// WithIndexPublisher 注入「视频索引事件」的发布者。
//
// 索引发布者挂在 QueryService 上（转码回写也要用到），这里只做转发。
func WithIndexPublisher(p IndexPublisher) Option {
	return func(s *Service) { s.QueryService.indexPub = p }
}

// Service 组合仓储、存储、上传配置与转码任务生产者。
//
// 内嵌 QueryService：视频域的只读查询（详情 / 列表 / 首页候选 / 转码状态读写）
// 与其写能力在同一进程内共用同一个仓储，gRPC 服务端只需依赖本类型即可覆盖整个视频域。
type Service struct {
	*QueryService
	repo      *repository.Repository
	store     storage.Storage
	cfg       config.UploadConfig
	transcode config.TranscodeConfig
	producer  *mq.Producer
	// deletePub 用于在删除视频后通知互动域清理关联数据（可为 nil）。
	deletePub DeletePublisher
	// cleanupLock 是过期上传清理任务的单飞锁：多副本同时在线时，同一轮只允许一个实例执行。
	// Redis 不可用时为 nil，退化为「多副本各跑一轮」——重复执行幂等，无正确性影响。
	cleanupLock *lock.Mutex
	// slots 是转码队列配额的信号量（见 reserveTranscodeSlot）。Redis 不可用时为 nil，
	// 退化为「只读库中在转数」的软约束。
	slots *semaphore.Semaphore
}

// New 构造 Service。
//
// store 屏蔽本地磁盘与对象存储的差异；producer 用于把转码任务投递到消息队列，
// 是上传与转码解耦的关键。rdb 供清理任务的单飞锁与转码配额预约使用；传 nil 表示禁用这两者
// （Redis 不可用时不阻断服务启动，与网关限流的 fail-open 取向一致）。
//
// opts 注入可选依赖（当前只有「视频删除事件发布者」）。
func New(repo *repository.Repository, store storage.Storage, cfg config.UploadConfig, transcode config.TranscodeConfig, producer *mq.Producer, rdb *redis.Client, opts ...Option) *Service {
	svc := &Service{
		QueryService: NewQueryService(repo, rdb),
		repo:         repo,
		store:        store,
		cfg:          cfg,
		transcode:    transcode,
		producer:     producer,
	}
	if rdb != nil {
		svc.cleanupLock = lock.New(rdb, cleanupLockKey, cleanupLockTTL)
		svc.slots = semaphore.New(rdb, transcodeSlotKey, transcode.MaxPending, transcodeSlotLease)
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Upload 处理管理员上传视频。
//
// 同步路径只做「校验 -> 落盘 -> 登记 -> 投递任务」四件事后立即返回，
// 真正的转码交给独立的 Worker 异步完成。因此这条记录初始状态为「转码中」，
// 前台在转码完成前看不到它。
func (s *Service) Upload(ctx context.Context, uploaderID int64, in dto.UploadInput) (*dto.VideoInfo, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("标题不能为空")
	}

	// 背压闸门放在最前面：队列满了就不该再让人把几百 MB 传上来再被拒。
	// 拿到的是「预约」而非一次性判定——它在整段落盘期间一直占着这个槽位，
	// 收尾时才归还，因此并发上传不会重复取走同一份余量（见 reserveTranscodeSlot）。
	release, err := s.reserveTranscodeSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ext, err := s.validateExt(in.Filename, s.cfg.AllowedExt)
	if err != nil {
		return nil, err
	}
	if err := s.validateSize(in.Size); err != nil {
		return nil, err
	}

	// 保存源文件。若后续步骤失败，需要把已落盘的文件清理掉，避免产生孤儿文件。
	sourceURL, err := s.saveFile(ctx, in.File, ext, in.Size)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			s.deleteObject(ctx, sourceURL)
		}
	}()

	coverURL := ""
	if in.Cover != nil {
		coverExt, err := s.validateExt(in.CoverFilename, coverExts)
		if err != nil {
			return nil, err
		}
		if err := s.validateSize(in.CoverSize); err != nil {
			return nil, err
		}
		coverURL, err = s.saveFile(ctx, in.Cover, coverExt, in.CoverSize)
		if err != nil {
			return nil, err
		}
	}

	info, err := s.finalizeSource(ctx, uploaderID, sourceURL, uploadMeta{
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		CategoryID:  in.CategoryID,
		CoverURL:    coverURL,
	})
	if err != nil {
		return nil, err
	}

	success = true
	return info, nil
}

// uploadMeta 描述一次上传的元信息，供单文件上传与分片上传共用。
type uploadMeta struct {
	Title       string
	Description string
	CategoryID  int64
	CoverURL    string
}

// reserveTranscodeSlot 预约一个转码槽位，返回交还配额的回调（调用方必须 defer 它）。
//
// 为什么用背压而不是「上传接口限流」：本平台的上传只开放给管理员，限 QPS 没有意义；
// 真正的瓶颈是转码——CPU 密集型且 Worker 串行消费，收下只会让作者等更久、让重试堆积。
// 因此在入口直接返回「服务繁忙」，好在**传输字节之前**就失败。
//
// 为什么需要预约而不是只读一次计数：背压闸门要在**落盘之前**决定收不收，
// 而真正占用队列发生在落盘**之后**。这中间的窗口（合并大文件的耗时可达分钟级）里，
// 只做「读计数 → 判断 → 继续」会形成检查-使用竞态（TOCTOU）：N 个上传同时收尾时
// 全都读到同一份余量、一起放行，把上限超卖。互斥锁在这里不适用（临界区横跨整段落盘，
// 会把上传串行化），因此用带租期的计数信号量（pkg/semaphore）：判断与占位一次 Lua 原子完成。
//
// 真实占用 = 库里「转码中」的记录数 + 在途预约数，两者相加才是队列深度：
// 前者由 Worker 的完成回写来释放，后者由本次上传收尾时 Release 归还。
//
// 三条降级路径（都不阻断上传）：
//   - MaxPending <= 0：不限制，返回空操作；
//   - 未接入 Redis：退回改造前的「只读库中在转数」软约束；
//   - Redis 访问失败：同样退回软约束（fail-open），与网关限流、清理单飞锁取向一致
//     ——背压是保护措施，不该因为 Redis 抖动就把上传全部拒掉。
func (s *Service) reserveTranscodeSlot(ctx context.Context) (func(), error) {
	noop := func() {}

	if s.transcode.MaxPending <= 0 {
		return noop, nil
	}

	pending, err := s.repo.CountByStatus(ctx, model.StatusTranscoding)
	if err != nil {
		return nil, err
	}

	// 降级路径共用的判定：没有信号量兜底时，只能按库里已有的在转数来判断。
	fallback := func() (func(), error) {
		if pending >= int64(s.transcode.MaxPending) {
			return nil, errcode.ErrServerBusy.WithMsg("转码队列已满，请稍后再试")
		}
		return noop, nil
	}

	if s.slots == nil {
		return fallback()
	}

	lease, ok, err := s.slots.TryAcquire(ctx, int(pending))
	if err != nil {
		logger.L().Warn("转码配额预约失败，退回仅按库中在转数判断",
			logger.TraceField(ctx), zap.Error(err))
		return fallback()
	}
	if !ok {
		return nil, errcode.ErrServerBusy.WithMsg("转码队列已满，请稍后再试")
	}

	return func() {
		// 用 WithoutCancel：调用方的 ctx 可能已被取消（客户端断开 / 超时），
		// 但配额必须还回去——否则要等租期到期，别的上传才能用这个槽位。
		if _, err := lease.Release(context.WithoutCancel(ctx)); err != nil {
			logger.L().Warn("交还转码配额失败，将由租期到期后自动回收",
				logger.TraceField(ctx), zap.Error(err))
		}
	}, nil
}

// finalizeSource 是上传收尾的公共步骤：登记视频记录并投递转码任务。
// 投递失败会回滚数据库记录，避免留下永远停在「转码中」的孤儿记录。
func (s *Service) finalizeSource(ctx context.Context, uploaderID int64, sourceURL string, meta uploadMeta) (*dto.VideoInfo, error) {
	v := &model.Video{
		ID:          snowflake.NextID(),
		Title:       meta.Title,
		Description: meta.Description,
		CoverURL:    meta.CoverURL,
		SourceURL:   sourceURL,
		// 播放地址由转码 Worker 产出 HLS 后回写，此处先留空
		PlayURL:    "",
		CategoryID: meta.CategoryID,
		UploaderID: uploaderID,
		Status:     model.StatusTranscoding,
	}
	if err := s.repo.Create(ctx, v); err != nil {
		return nil, err
	}

	// 投递转码任务：投递失败时回滚数据库记录，否则会留下一条永远停在
	// 「转码中」的孤儿记录。
	task := transcodedto.Task{VideoID: v.ID, SourceURL: sourceURL, Attempt: 1}
	if err := s.producer.Publish(ctx, strconv.FormatInt(v.ID, 10), task); err != nil {
		_ = s.repo.Delete(ctx, v.ID)
		return nil, errcode.ErrUploadFailed.WithMsg("转码任务投递失败，请稍后重试")
	}

	return toVideoInfo(v), nil
}

// RetryTranscode 重新转码一个失败视频。
//
// 只在「转码失败」状态下允许：源文件在上传时就落盘了（失败不删源），
// 所以重试不需要管理员再传一遍几百 MB。流程与上传收尾一致：
// **占转码槽位 → 状态打回「转码中」并清空失败原因 → 重新投递任务 → 交还槽位**。
//
// 两个必须守住的点：
//   - 槽位不能省：重试与上传同样会往队列里塞任务，不占位就成了绕过背压的旁路；
//   - 投递失败要还原状态：否则视频停在「转码中」，既不会被 Worker 处理（任务没发出去），
//     也不会再满足「只有失败可重试」的条件——只能删了重传。
func (s *Service) RetryTranscode(ctx context.Context, id int64) (*dto.VideoInfo, error) {
	v, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrVideoNotFound
	}
	if err != nil {
		return nil, err
	}
	if v.Status != model.StatusFailed {
		return nil, errcode.ErrInvalidParam.WithMsg("只有转码失败的视频可以重新转码")
	}
	if v.SourceURL == "" {
		// 理论上不会发生（失败时源文件保留），但缺了源文件就无从重转，明确报错比静默失败好。
		return nil, errcode.ErrInvalidParam.WithMsg("源文件地址缺失，无法重新转码")
	}

	release, err := s.reserveTranscodeSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	previousErr := v.ErrorMsg
	if err := s.repo.UpdateFields(ctx, id, map[string]any{
		"status":    model.StatusTranscoding,
		"error_msg": "",
	}); err != nil {
		return nil, err
	}

	task := transcodedto.Task{VideoID: v.ID, SourceURL: v.SourceURL, Attempt: 1}
	if err := s.producer.Publish(ctx, strconv.FormatInt(v.ID, 10), task); err != nil {
		// 还原失败态（连同原来的失败原因），让管理员可以再点一次。
		_ = s.repo.UpdateFields(ctx, id, map[string]any{
			"status":    model.StatusFailed,
			"error_msg": previousErr,
		})
		return nil, errcode.ErrServerBusy.WithMsg("转码任务投递失败，请稍后重试")
	}

	updated, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toVideoInfo(updated), nil
}

// AdminList 分页查询全部视频（含未上架），供管理后台使用。
func (s *Service) AdminList(ctx context.Context, q dto.AdminListQuery) ([]dto.VideoInfo, int64, int, int, error) {
	page, pageSize, offset := utils.NormalizePage(q.Page, q.PageSize)

	videos, total, err := s.repo.ListAll(ctx, q.Status, strings.TrimSpace(q.Keyword), offset, pageSize)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	list := make([]dto.VideoInfo, 0, len(videos))
	for i := range videos {
		list = append(list, *toVideoInfo(&videos[i]))
	}
	return list, total, page, pageSize, nil
}

// UpdateStatus 上下架视频。
//
// 只接受「已上架」与「已下架」两个目标状态：转码中是上传流程的中间态，
// 不应该由管理后台手动设置。状态未变化时直接返回当前信息，避免无意义的写库。
func (s *Service) UpdateStatus(ctx context.Context, id int64, status int8) (*dto.VideoInfo, error) {
	if status != model.StatusPublished && status != model.StatusOffline {
		return nil, errcode.ErrInvalidParam.WithMsg("状态只能是 1（上架）或 2（下架）")
	}

	v, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrVideoNotFound
	}
	if err != nil {
		return nil, err
	}
	if v.Status == status {
		return toVideoInfo(v), nil
	}

	if err := s.repo.UpdateStatus(ctx, id, status); err != nil {
		return nil, err
	}
	v.Status = status
	// 上架 / 下架的可见性变化必须同步到检索索引。
	s.publishVideoIndex(ctx, v)
	return toVideoInfo(v), nil
}

// Update 编辑视频元信息（标题 / 简介 / 分类 / 封面）。
//
// 片源不可替换。封面只在传入时替换，且新封面落盘、数据库切换引用之后
// 才删除旧封面，避免出现「库里指向新图、新图没落盘」或孤儿文件。
func (s *Service) Update(ctx context.Context, id int64, in dto.UpdateVideoInput) (*dto.VideoInfo, error) {
	v, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrVideoNotFound
	}
	if err != nil {
		return nil, err
	}

	fields := make(map[string]any, 4)
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return nil, errcode.ErrInvalidParam.WithMsg("标题不能为空")
		}
		fields["title"] = title
	}
	if in.Description != nil {
		// 传空串是「清空简介」的合法操作，不做非空校验
		fields["description"] = strings.TrimSpace(*in.Description)
	}
	if in.CategoryID != nil {
		fields["category_id"] = *in.CategoryID
	}

	oldCover := v.CoverURL
	if in.Cover != nil {
		coverExt, err := s.validateExt(in.CoverFilename, coverExts)
		if err != nil {
			return nil, err
		}
		if err := s.validateSize(in.CoverSize); err != nil {
			return nil, err
		}
		coverURL, err := s.saveFile(ctx, in.Cover, coverExt, in.CoverSize)
		if err != nil {
			return nil, err
		}
		fields["cover_url"] = coverURL
	}

	if err := s.repo.UpdateFields(ctx, id, fields); err != nil {
		// 写库失败：把刚落盘的新封面删掉，旧封面与旧数据保持原样
		if newCover, ok := fields["cover_url"].(string); ok {
			s.deleteObject(ctx, newCover)
		}
		return nil, err
	}

	// 引用已经切走，旧封面可以安全清理
	if _, replaced := fields["cover_url"]; replaced {
		s.deleteObject(ctx, oldCover)
	}

	updated, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// 标题 / 简介 / 分类都是检索字段，编辑后同步索引；
	// 未上架的视频在这里会被移除（虽然它本来也不在索引里，幂等）。
	s.publishVideoIndex(ctx, updated)
	return toVideoInfo(updated), nil
}

// Delete 删除视频：先删数据库记录，再清理磁盘文件。
//
// 顺序上先删记录：即便文件清理失败（例如文件已被手工移走），
// 库里也不会留下指向不存在文件的记录。文件清理是尽力而为，失败了也不影响删除结果。
func (s *Service) Delete(ctx context.Context, id int64) error {
	v, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errcode.ErrVideoNotFound
	}
	if err != nil {
		return err
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	// 记录已删，先把它从检索索引里移除，避免搜索结果继续命中一个打不开的详情页。
	s.publishVideoIndexDelete(ctx, id)

	// 源文件与封面可能落在本地磁盘或对象存储，统一按驱动反解 key 后删除。
	for _, url := range []string{v.PlayURL, v.SourceURL, v.CoverURL} {
		s.deleteObject(ctx, url)
	}
	// HLS 产物是一整棵按视频 ID 命名的本地目录，单独整棵清理
	_ = os.RemoveAll(filepath.Join(s.transcode.OutputDir, strconv.FormatInt(id, 10)))

	// 投递删除事件，让互动域清掉这个视频的评论 / 弹幕 / 点赞 / 收藏 / 稍后再看 / 历史与站内通知。
	//
	// 失败只记日志、不影响删除结果：视频记录已经删了，让接口报错只会让管理员对着
	// 「视频不存在」反复重试。孤儿互动行不会破坏任何读路径（列表会按视频是否存在过滤），
	// 但会占库，所以这里必须留下可排查的日志。
	if s.deletePub != nil {
		if err := s.deletePub.PublishVideoDeleted(ctx, id); err != nil {
			logger.L().Error("投递视频删除事件失败，互动数据需人工清理",
				zap.Int64("video_id", id), zap.Error(err))
		}
	}
	return nil
}

// validateExt 校验文件扩展名是否在白名单内，返回规范化后的扩展名。
func (s *Service) validateExt(filename string, allowed []string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "", errcode.ErrFileTypeDenied
	}
	for _, a := range allowed {
		if ext == strings.ToLower(a) {
			return ext, nil
		}
	}
	return "", errcode.ErrFileTypeDenied
}

// validateSize 校验文件大小是否超出上限。
func (s *Service) validateSize(size int64) error {
	if s.cfg.MaxSizeMB <= 0 {
		return nil
	}
	if size > int64(s.cfg.MaxSizeMB)*1024*1024 {
		return errcode.ErrFileTooLarge.WithMsg(
			fmt.Sprintf("文件超出大小限制（最大 %d MB）", s.cfg.MaxSizeMB),
		)
	}
	return nil
}

// saveFile 把上传内容写入存储，返回可访问的 URL。
//
// size 为内容的字节数：对象存储的单次写入需要已知长度，调用方必须传真实大小。
func (s *Service) saveFile(ctx context.Context, r io.Reader, ext string, size int64) (string, error) {
	key := strconv.FormatInt(snowflake.NextID(), 10) + ext
	if err := s.store.Put(ctx, key, r, size, ""); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}
	return s.store.URL(key), nil
}

// deleteObject 按对外地址反解对象 key 并删除；地址不属于当前驱动时静默跳过。
//
// 清理属于尽力而为：失败不影响调用方的业务结果（例如视频记录已删除）。
func (s *Service) deleteObject(ctx context.Context, url string) {
	if url == "" {
		return
	}
	if key, ok := s.store.KeyFromURL(url); ok {
		_ = s.store.Delete(ctx, key)
	}
}

// toVideoInfo 把数据模型转换为对外 DTO。
func toVideoInfo(v *model.Video) *dto.VideoInfo {
	return &dto.VideoInfo{
		ID:            v.ID,
		Title:         v.Title,
		Description:   v.Description,
		CoverURL:      v.CoverURL,
		PlayURL:       v.PlayURL,
		Duration:      v.Duration,
		CategoryID:    v.CategoryID,
		ViewCount:     v.ViewCount,
		LikeCount:     v.LikeCount,
		FavoriteCount: v.FavoriteCount,
		Status:        v.Status,
		ErrorMsg:      v.ErrorMsg,
		CreatedAt:     v.CreatedAt,
	}
}
