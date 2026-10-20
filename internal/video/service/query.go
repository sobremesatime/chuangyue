// Package service 提供视频信息的只读查询服务。
package service

import (
	"context"
	"strings"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// QueryService 是视频信息的薄服务。
//
// 绝大多数方法只做只读委托，便于被 gRPC 与其它调用方复用，也不与上传等写入流程（Service）耦合；
// 唯二例外是转码回写能力：转码 Worker 独立成进程后不再直连视频库，其写路径（ReportTranscodeResult）
// 与幂等读（GetByID 供 GetTranscodeState 用）统一经本服务收口。
type QueryService struct {
	repo *repository.Repository
	// viewCounter 是播放量的 Redis 缓冲（见 view_count.go）。rdb 不可用时为 nil，
	// 此时播放量退回「每次直接写库」。
	viewCounter *ViewCounter
	// indexPub 把视频的检索元信息同步到搜索域（可为 nil：未启用 ES 的部署不需要）。
	// 放在 QueryService 上是因为转码回写（ReportTranscodeResult）也会改变上下架状态。
	indexPub IndexPublisher
}

// NewQueryService 构造 QueryService。rdb 为 nil 表示未接入 Redis，播放量走直接写库路径。
func NewQueryService(repo *repository.Repository, rdb *redis.Client) *QueryService {
	qs := &QueryService{repo: repo}
	if rdb != nil {
		qs.viewCounter = newViewCounter(rdb, repo)
	}
	return qs
}

// GetByID 按主键查询单个视频，未找到时返回 gorm.ErrRecordNotFound。
func (s *QueryService) GetByID(ctx context.Context, id int64) (*model.Video, error) {
	return s.repo.GetByID(ctx, id)
}

// GetByIDs 批量按主键查询视频；入参为空返回空切片。
func (s *QueryService) GetByIDs(ctx context.Context, ids []int64) ([]model.Video, error) {
	return s.repo.GetByIDs(ctx, ids)
}

// ListFeedCandidates 取首页候选池：已上架视频按热度倒序取前 limit 条。
func (s *QueryService) ListFeedCandidates(ctx context.Context, limit int) ([]model.Video, error) {
	return s.repo.ListFeedCandidates(ctx, limit)
}

// 视频浏览列表的排序口径，与 gRPC 契约里的 sort 取值一一对应。
const (
	sortLatest int32 = 0 // 按最新发布倒序（默认）
	sortHot    int32 = 1 // 按热度倒序
)

// ListVideos 分页查询已上架视频，供网关的 GET /videos 使用。
//
// 负责参数归一（page < 1 取 1、pageSize 非法取默认值并封顶）与 page→offset 换算，
// 再委托 ListPublished；归一逻辑复用 utils.NormalizePage，与进程内的列表查询口径一致。
func (s *QueryService) ListVideos(ctx context.Context, categoryID int64, keyword string, sort int32, page, pageSize int) ([]model.Video, int64, error) {
	_, normPageSize, offset := utils.NormalizePage(page, pageSize)

	order := repository.OrderLatest
	if sort == sortHot {
		order = repository.OrderHot
	}

	// 与进程内 List 一致：关键词去掉首尾空白，为空则不参与过滤。
	return s.repo.ListPublished(ctx, categoryID, strings.TrimSpace(keyword), order, offset, normPageSize)
}

// IncrViewCount 播放量自增，供详情页统计。
//
// 由读接口触发，属尽力而为的统计：失败由调用方决定是否忽略，不影响详情返回。
// 有 Redis 时只把增量累加进缓冲（由定时任务批量落库），读路径上不再有同步写；
// 没有 Redis 时退回直接 UPDATE，保证计数不丢。
func (s *QueryService) IncrViewCount(ctx context.Context, id int64) error {
	if s.viewCounter == nil {
		return s.repo.IncrViewCount(ctx, id)
	}
	return s.viewCounter.Incr(ctx, id)
}

// ReportTranscodeResult 回写转码结果：播放地址、时长、状态与失败原因。
//
// 仅供内部 RPC ReportTranscodeResult 委托调用，使转码 Worker 的写路径不再直连视频库。
// 转码成功会把状态改为「已上架」，因此这里还要补一次索引同步——否则新视频要等到
// 管理员手动上下架一次才会出现在搜索结果里。
func (s *QueryService) ReportTranscodeResult(ctx context.Context, videoID int64, playURL string, duration int, status int8, errMsg string) error {
	if err := s.repo.UpdateTranscodeResult(ctx, videoID, playURL, duration, status, errMsg); err != nil {
		return err
	}

	// 回写成功后再取一次最新记录用于索引事件。取不到只告警、不影响 RPC 结果：
	// 转码结果已经落库，让 Worker 因索引同步失败而重试，代价远大于索引短暂滞后。
	updated, err := s.repo.GetByID(ctx, videoID)
	if err != nil {
		logger.L().Warn("回写转码结果后读取视频失败，检索索引可能滞后",
			logger.TraceField(ctx), zap.Int64("video_id", videoID), zap.Error(err))
		return nil
	}
	s.publishVideoIndex(ctx, updated)
	return nil
}

// publishVideoIndex 把视频当前状态同步到检索索引。
//
// 口径：已上架 → 覆盖写索引；其余状态（转码中 / 下架 / 失败）→ 从索引移除。
// 索引只服务普通用户的检索，未上架的视频本就不该出现在结果里。
func (s *QueryService) publishVideoIndex(ctx context.Context, v *model.Video) {
	if s.indexPub == nil || v == nil {
		return
	}

	event := dto.VideoIndexEvent{Action: dto.VideoIndexDelete, VideoID: v.ID}
	if v.Status == model.StatusPublished {
		event.Action = dto.VideoIndexUpsert
		event.Video = &dto.VideoIndexDoc{
			ID:          v.ID,
			Title:       v.Title,
			Description: v.Description,
			CategoryID:  v.CategoryID,
			CreatedAt:   v.CreatedAt,
		}
	}

	// 与删除事件同口径：索引是派生数据，投递失败只记错误日志，不回滚业务写入。
	// Kafka 恢复后管理员重新上下架 / 编辑一次即可补上；索引也支持启动时全量回填。
	if err := s.indexPub.PublishVideoIndex(ctx, event); err != nil {
		logger.L().Error("投递视频索引事件失败，检索结果可能滞后",
			logger.TraceField(ctx), zap.Int64("video_id", v.ID), zap.Error(err))
	}
}

// publishVideoIndexDelete 从索引中移除一条视频（下架 / 删除）。
func (s *QueryService) publishVideoIndexDelete(ctx context.Context, videoID int64) {
	s.publishVideoIndex(ctx, &model.Video{ID: videoID, Status: model.StatusOffline})
}
