//go:build integration

// 转码配额预约的集成测试：需要本地 MySQL 与 Redis，任一不可用则跳过。
//
//	go test -tags integration -v -run TestReserveTranscodeSlot ./internal/video/service/...
//
// 两个前提：
//   - **MySQL 口令必须由环境变量提供**（`MYSQL_PASSWORD`），文件里不落任何口令——
//     本地 compose 起得来时可以直接从容器读：
//     `$env:MYSQL_PASSWORD = (docker exec chuangyue-mysql printenv MYSQL_ROOT_PASSWORD)`；
//   - 测试会清空被测的信号量键（`video:transcode:slots`），因此只应在本地开发栈上跑。
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/mq"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/storage"
)

// newSlotTestDB 自包含地连本地 MySQL，连不上或未提供口令则跳过。
func newSlotTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	password := os.Getenv("MYSQL_PASSWORD")
	if password == "" {
		t.Skip("未设置 MYSQL_PASSWORD，跳过集成测试（口令只从环境变量取，不落文件）")
	}

	cfg := config.MySQLConfig{
		Host:         slotEnvOr("MYSQL_HOST", "127.0.0.1"),
		Port:         slotEnvIntOr("MYSQL_PORT", 3306),
		Username:     slotEnvOr("MYSQL_USER", "root"),
		Password:     password,
		Database:     slotEnvOr("MYSQL_DATABASE", "chuangyue"),
		Charset:      "utf8mb4",
		MaxIdleConns: 2,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	}
	db, err := database.NewMySQL(cfg)
	if err != nil {
		t.Skipf("MySQL 不可用，跳过集成测试: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

func slotEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func slotEnvIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// newSlotTestRedis 连本地 Redis（可用 REDIS_ADDR 覆盖），不可用则跳过。
func newSlotTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	addr := slotEnvOr("REDIS_ADDR", "127.0.0.1:6379")
	rdb := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("Redis 不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// countTranscoding 返回库里当前「转码中」的记录数——即背压要与之相加的那部分占用。
func countTranscoding(t *testing.T, db *gorm.DB) int {
	t.Helper()

	var n int64
	if err := db.Model(&model.Video{}).Where("status = ?", model.StatusTranscoding).Count(&n).Error; err != nil {
		t.Fatalf("统计转码中记录失败: %v", err)
	}
	return int(n)
}

// reserveConcurrently 并发调用 n 次 reserveTranscodeSlot，返回成功数与交还函数。
func reserveConcurrently(ctx context.Context, svc *Service, n int) (int, []func()) {
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		releases  []func()
	)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 尽量同时发起，最大化竞争
			release, err := svc.reserveTranscodeSlot(ctx)
			if err != nil {
				return // 队列满时这里返回业务错误，计为「未放行」
			}
			mu.Lock()
			succeeded++
			releases = append(releases, release)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	return succeeded, releases
}

// TestReserveTranscodeSlot_NoOversellUnderConcurrency 是本项改造的核心验收：
// 多个上传同时收尾时，放行数必须恰好等于余量，一个都不能多。
//
// 改造前的实现是「读一次库里的在转数 → 判断 → 继续」，这些并发调用会**全部读到
// 同一份余量**而一起放行（超卖）；现在判断与占位在一次 Lua 里原子完成，超卖不再可能。
func TestReserveTranscodeSlot_NoOversellUnderConcurrency(t *testing.T) {
	db := newSlotTestDB(t)
	rdb := newSlotTestRedis(t)
	ctx := context.Background()

	// 上限取「库中在转数 + 2」：无论库里原本有几条在转记录，余量都是 2。
	before := countTranscoding(t, db)
	const free = 2
	svc := New(videorepo.New(db), nil, config.UploadConfig{},
		config.TranscodeConfig{MaxPending: before + free}, nil, rdb)

	// 清空信号量键：上一次运行 / 测试残留的预约会干扰这条断言。
	if err := rdb.Del(ctx, transcodeSlotKey).Err(); err != nil {
		t.Fatalf("清理信号量键失败: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Del(context.Background(), transcodeSlotKey).Err() })

	succeeded, releases := reserveConcurrently(ctx, svc, 16)

	// 核心不变量：库中在转数 + 放行数不得超过上限。这一条与环境是否安静无关，永远要成立。
	after := countTranscoding(t, db)
	if after+succeeded > before+free {
		t.Fatalf("超卖：库中在转数 %d + 并发放行 %d > 上限 %d", after, succeeded, before+free)
	}

	// 占满后再来一个也必须被拒。
	if _, err := svc.reserveTranscodeSlot(ctx); err == nil {
		t.Fatal("余量耗尽后应拒绝新上传")
	}

	// 库中在转数若在本用例期间被外部进程改动（典型是转码 Worker 完成回写），
	// 「恰好放行 free 个」就失去基准——此时只保留上面的不变量断言。
	if after != before {
		t.Skipf("库中在转数在用例期间被外部改动（%d → %d），跳过精确计数断言", before, after)
	}
	if succeeded != free {
		t.Fatalf("库中在转数稳定时，放行数应恰好为余量 %d，实际 %d", free, succeeded)
	}

	// 全部交还后，余量应重新可用——验证 Release 确实把配额还回去了。
	for _, release := range releases {
		release()
	}
	if _, err := svc.reserveTranscodeSlot(ctx); err != nil {
		t.Fatalf("配额交还后应能再次预约: %v", err)
	}
}

// slotLimitNotBinding 是一个大到不会被触发的上限，用于只关心其它行为的用例。
const slotLimitNotBinding = 1 << 20

// TestReserveTranscodeSlot_FallsBackWithoutRedis 覆盖降级路径：
// 未接入 Redis 时退回改造前的「只读库中在转数」软约束，上传不被阻断。
func TestReserveTranscodeSlot_FallsBackWithoutRedis(t *testing.T) {
	db := newSlotTestDB(t)
	ctx := context.Background()

	// 先造一条「转码中」记录，好让「已满」由本用例自己造出来，不依赖库里原有数据。
	sample := &model.Video{
		ID:         snowflake.NextID(),
		Title:      "slot-it-fallback-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		UploaderID: slotTestAdminID,
		Status:     model.StatusTranscoding,
	}
	if err := db.Create(sample).Error; err != nil {
		t.Fatalf("插入在转记录失败: %v", err)
	}
	t.Cleanup(func() { db.Where("id = ?", sample.ID).Delete(&model.Video{}) })

	repo := videorepo.New(db)

	// 上限设 1 且库中已有 1 条在转 → 必定已满，应按软约束拒绝。
	// 用固定值而不是「读到的在转数」：外部进程（转码 Worker 完成回写）改动在转数时，结果不会漂移。
	full := New(repo, nil, config.UploadConfig{}, config.TranscodeConfig{MaxPending: 1}, nil, nil)
	if _, err := full.reserveTranscodeSlot(ctx); err == nil {
		t.Fatal("无 Redis 且库中已满时应拒绝")
	}

	// 上限给足 → 应放行（并返回空操作的交还函数）。
	room := New(repo, nil, config.UploadConfig{},
		config.TranscodeConfig{MaxPending: slotLimitNotBinding}, nil, nil)
	release, err := room.reserveTranscodeSlot(ctx)
	if err != nil {
		t.Fatalf("无 Redis 且未满时应放行: %v", err)
	}
	release() // 空操作，不应 panic
}

// slotTestAdminID 是所有测试用的上传者 ID；归属校验只比对会话上的 user_id，用一个固定值即可。
const slotTestAdminID int64 = 1

// slotUploadConfig 是分片上传测试用的最小配置：单片即传完，只允许 .mp4。
func slotUploadConfig(localDir string) config.UploadConfig {
	return config.UploadConfig{
		Driver:      "local",
		LocalDir:    localDir,
		MaxSizeMB:   1,
		ChunkSizeMB: 1,
		AllowedExt:  []string{".mp4"},
	}
}

// initAndUploadOnePart 走一遍真实的分片上传前置步骤（建会话 + 传完整片），
// 返回会话 ID 与本次用的标题（标题全程唯一，供断言孤儿记录时精确定位）。
//
// 用真实的文件指纹：秒传与断点续传都以它为键，随便填会导致误命中别的会话。
func initAndUploadOnePart(t *testing.T, db *gorm.DB, svc *Service, ctx context.Context) (int64, string) {
	t.Helper()

	title := "slot-it-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	sum := sha256.Sum256([]byte(title))
	md5 := hex.EncodeToString(sum[:16]) // 32 位十六进制，符合前端算出的指纹格式

	resp, err := svc.InitUpload(ctx, slotTestAdminID, dto.InitUploadRequest{
		FileMD5:     md5,
		Filename:    "slot-it.mp4",
		TotalSize:   1024,
		ContentType: "video/mp4",
		Title:       title,
	})
	if err != nil {
		t.Fatalf("初始化分片上传失败: %v", err)
	}
	if resp.Mode != dto.UploadModeMultipart {
		t.Fatalf("期望进入分片上传模式，实际 %s", resp.Mode)
	}
	uploadID, err := strconv.ParseInt(resp.UploadID, 10, 64)
	if err != nil {
		t.Fatalf("解析上传会话 ID 失败: %v", err)
	}
	// 会话与分片明细都是真实写进库的，测试结束要清掉。
	t.Cleanup(func() {
		db.Where("session_id = ?", uploadID).Delete(&model.UploadPart{})
		db.Where("id = ?", uploadID).Delete(&model.UploadSession{})
	})

	payload := make([]byte, 1024)
	if _, err := svc.PutPart(ctx, slotTestAdminID, uploadID, 1,
		bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("上传分片失败: %v", err)
	}
	return uploadID, title
}

// TestCompleteUpload_ReturnsSlotOnPublishFailure 覆盖「预约覆盖整段合并 + 收尾」这条接线：
// 收尾失败（投递转码任务失败）时，配额也必须归还——否则失败几次之后队列就再也进不来了。
//
// 这里把生产者指向一个不可达的 broker，让投递必然失败，从而稳定地走到失败分支；
// 顺带验证 finalizeSource 的回滚不会留下停在「转码中」的孤儿记录。
func TestCompleteUpload_ReturnsSlotOnPublishFailure(t *testing.T) {
	db := newSlotTestDB(t)
	rdb := newSlotTestRedis(t)
	ctx := context.Background()

	// 上限给足，确保这条用例不会因为队列满而提前返回——要考的是「失败路径也归还配额」。
	localDir := t.TempDir()
	// 不可达的 broker：不需要真的 Kafka，就能稳定复现「收尾失败」。
	producer := mq.NewProducer([]string{"127.0.0.1:1"}, "chuangyue.transcode.task.it")
	t.Cleanup(func() { _ = producer.Close() })

	svc := New(videorepo.New(db), storage.NewLocal(localDir), slotUploadConfig(localDir),
		config.TranscodeConfig{MaxPending: slotLimitNotBinding}, producer, rdb)
	if err := rdb.Del(ctx, transcodeSlotKey).Err(); err != nil {
		t.Fatalf("清理信号量键失败: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Del(context.Background(), transcodeSlotKey).Err() })

	uploadID, title := initAndUploadOnePart(t, db, svc, ctx)
	t.Cleanup(func() { db.Where("title = ?", title).Delete(&model.Video{}) })

	if _, err := svc.CompleteUpload(ctx, slotTestAdminID, uploadID, dto.CompleteUploadRequest{}); err == nil {
		t.Fatal("投递转码任务失败时 CompleteUpload 应返回错误")
	}

	// 关键断言：失败路径上配额也归还了。
	if used, err := svc.slots.Used(ctx); err != nil || used != 0 {
		t.Fatalf("收尾失败后应归还配额: used=%d err=%v", used, err)
	}
	// 回滚检查：不应留下停在「转码中」的孤儿记录（finalizeSource 的投递失败回滚）。
	// 按本次用例自己的标题精确匹配，不用 LIKE——否则会被其它用例 / 历史残留误伤。
	var orphan int64
	if err := db.Model(&model.Video{}).Where("title = ?", title).Count(&orphan).Error; err != nil {
		t.Fatalf("统计孤儿记录失败: %v", err)
	}
	if orphan != 0 {
		t.Fatalf("投递失败后不应留下记录，实际 %d 条（title=%s）", orphan, title)
	}
}

// TestCompleteUpload_RejectsWhenQueueFull 覆盖「闸门确实卡在合并之前」：
// 队列满时直接返回服务繁忙，不做合并、不登记记录、不占配额。
func TestCompleteUpload_RejectsWhenQueueFull(t *testing.T) {
	db := newSlotTestDB(t)
	rdb := newSlotTestRedis(t)
	ctx := context.Background()

	// 造一条「转码中」记录，并把上限设成 1 ——这样「已满」由本用例自己造出来，
	// 与库里原有数据、以及外部进程（转码 Worker 完成回写）的在转数变化都无关。
	sample := &model.Video{
		ID:         snowflake.NextID(),
		Title:      "slot-it-queue-full-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		UploaderID: slotTestAdminID,
		Status:     model.StatusTranscoding,
	}
	if err := db.Create(sample).Error; err != nil {
		t.Fatalf("插入在转记录失败: %v", err)
	}
	t.Cleanup(func() { db.Where("id = ?", sample.ID).Delete(&model.Video{}) })

	localDir := t.TempDir()
	// 生产者指向不可达地址：万一闸门没拦住，这条用例会以「投递失败」收场而不是空指针 panic，
	// 不会把一条停在「转码中」的记录留在库里。
	producer := mq.NewProducer([]string{"127.0.0.1:1"}, "chuangyue.transcode.task.it")
	t.Cleanup(func() { _ = producer.Close() })

	svc := New(videorepo.New(db), storage.NewLocal(localDir), slotUploadConfig(localDir),
		config.TranscodeConfig{MaxPending: 1}, producer, rdb)
	if err := rdb.Del(ctx, transcodeSlotKey).Err(); err != nil {
		t.Fatalf("清理信号量键失败: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Del(context.Background(), transcodeSlotKey).Err() })

	uploadID, title := initAndUploadOnePart(t, db, svc, ctx)
	t.Cleanup(func() { db.Where("title = ?", title).Delete(&model.Video{}) })

	_, err := svc.CompleteUpload(ctx, slotTestAdminID, uploadID, dto.CompleteUploadRequest{})
	if err == nil {
		t.Fatal("队列已满时应拒绝收尾")
	}
	var ec errcode.ErrCode
	if !errors.As(err, &ec) || ec.Code != errcode.ErrServerBusy.Code {
		t.Fatalf("应返回「服务繁忙」业务码，实际: %v", err)
	}
	if used, _ := svc.slots.Used(ctx); used != 0 {
		t.Fatalf("被拒绝的上传不应占用配额: used=%d", used)
	}
}

// newInstantSessionFixture 造一份「已有相同指纹的已完成会话」，供秒传分支使用。
// 返回文件指纹与本次用的标题（标题唯一，便于精确断言孤儿记录）。
//
// 秒传的命中条件是 (file_md5, status=completed)，因此直接插一条会话即可，
// 不必真的走完一次分片上传。
func newInstantSessionFixture(t *testing.T, db *gorm.DB, ctx context.Context) (md5, title string) {
	t.Helper()

	title = "slot-it-instant-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	sum := sha256.Sum256([]byte(title))
	md5 = hex.EncodeToString(sum[:16]) // 32 位十六进制，符合前端算出的指纹格式

	session := &model.UploadSession{
		ID:          snowflake.NextID(),
		UserID:      slotTestAdminID,
		FileMD5:     md5,
		Filename:    "slot-it.mp4",
		ContentType: "video/mp4",
		Title:       title,
		TotalSize:   1024,
		ChunkSize:   1024,
		PartTotal:   1,
		StorageKey:  "video/source/" + md5 + ".mp4",
		Driver:      "local",
		UploadID:    "instant-it-" + md5,
		Status:      model.UploadStatusCompleted,
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if err := db.WithContext(ctx).Create(session).Error; err != nil {
		t.Fatalf("插入已完成上传会话失败: %v", err)
	}
	t.Cleanup(func() { db.Where("id = ?", session.ID).Delete(&model.UploadSession{}) })
	return md5, title
}

// countVideosByTitle 按标题统计视频记录数，用于孤儿 / 误登记记录的精确断言。
// 用标题精确匹配而不是 LIKE——否则会被其它用例或历史残留误伤。
func countVideosByTitle(t *testing.T, db *gorm.DB, title string) int64 {
	t.Helper()

	var n int64
	if err := db.Model(&model.Video{}).Where("title = ?", title).Count(&n).Error; err != nil {
		t.Fatalf("按标题统计视频记录失败: %v", err)
	}
	return n
}

// instantInitRequest 组装一次秒传初始化请求；指纹与标题由 fixture 提供。
func instantInitRequest(md5, title string) dto.InitUploadRequest {
	return dto.InitUploadRequest{
		FileMD5:     md5,
		Filename:    "slot-it.mp4",
		TotalSize:   1024,
		ContentType: "video/mp4",
		Title:       title,
	}
}

// TestInitUploadInstant_RejectsWhenQueueFull 覆盖「秒传分支也过背压闸门」。
//
// 秒传不传字节，但它同样要登记视频并投递转码任务（HLS 产物按 video_id 分目录，
// 复用源文件也得重新转一遍），所以照样占一个队列槽位。改造前这条分支直接
// finalizeSource、完全绕开闸门，队列满时重复文件仍能穿过去、队列深度被超卖。
//
// 断言返回的是「服务繁忙」业务码，正是因为闸门拦在了收尾之前：若没拦住，
// 就会一路走到投递失败，报出来的是「上传失败」而不是「服务繁忙」。
func TestInitUploadInstant_RejectsWhenQueueFull(t *testing.T) {
	db := newSlotTestDB(t)
	rdb := newSlotTestRedis(t)
	ctx := context.Background()

	// 造一条「转码中」记录并把上限设成 1 ——「已满」由本用例自己造出来，
	// 与库里原有数据、以及外部进程（转码 Worker 完成回写）的在转数变化都无关。
	sample := &model.Video{
		ID:         snowflake.NextID(),
		Title:      "slot-it-instant-full-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		UploaderID: slotTestAdminID,
		Status:     model.StatusTranscoding,
	}
	if err := db.Create(sample).Error; err != nil {
		t.Fatalf("插入在转记录失败: %v", err)
	}
	t.Cleanup(func() { db.Where("id = ?", sample.ID).Delete(&model.Video{}) })

	localDir := t.TempDir()
	// 生产者指向不可达地址：万一闸门没拦住，这条用例会以「投递失败」收场，
	// 而不是把一条停在「转码中」的记录留在库里。
	producer := mq.NewProducer([]string{"127.0.0.1:1"}, "chuangyue.transcode.task.it")
	t.Cleanup(func() { _ = producer.Close() })

	svc := New(videorepo.New(db), storage.NewLocal(localDir), slotUploadConfig(localDir),
		config.TranscodeConfig{MaxPending: 1}, producer, rdb)
	if err := rdb.Del(ctx, transcodeSlotKey).Err(); err != nil {
		t.Fatalf("清理信号量键失败: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Del(context.Background(), transcodeSlotKey).Err() })

	md5, title := newInstantSessionFixture(t, db, ctx)

	_, err := svc.InitUpload(ctx, slotTestAdminID, instantInitRequest(md5, title))
	if err == nil {
		t.Fatal("队列已满时秒传应被拒绝")
	}
	var ec errcode.ErrCode
	if !errors.As(err, &ec) || ec.Code != errcode.ErrServerBusy.Code {
		t.Fatalf("应返回「服务繁忙」业务码，实际: %v", err)
	}
	// 被拒的秒传不应登记记录，也不应占配额。
	if n := countVideosByTitle(t, db, title); n != 0 {
		t.Fatalf("被拒的秒传不应登记视频记录，实际 %d 条（title=%s）", n, title)
	}
	if used, _ := svc.slots.Used(ctx); used != 0 {
		t.Fatalf("被拒绝的秒传不应占用配额: used=%d", used)
	}
}

// TestInitUploadInstant_ReturnsSlotOnPublishFailure 覆盖秒传分支的「预约 → 收尾 → 归还」接线：
// 收尾失败（投递转码任务失败）时配额也必须归还——否则每次失败的秒传都会泄漏一个槽位，
// 队列会被幽灵占用一点点吃掉。
//
// 生产者指向不可达的 broker，让投递必然失败，从而稳定走到失败分支；
// 顺带验证 finalizeSource 的回滚不会留下停在「转码中」的孤儿记录。
func TestInitUploadInstant_ReturnsSlotOnPublishFailure(t *testing.T) {
	db := newSlotTestDB(t)
	rdb := newSlotTestRedis(t)
	ctx := context.Background()

	// 上限给足（队列有余量）：要考的是「过了闸门之后失败也归还配额」，
	// 而不是「闸门拦不拦得住」——后者由上一个用例覆盖。
	localDir := t.TempDir()
	producer := mq.NewProducer([]string{"127.0.0.1:1"}, "chuangyue.transcode.task.it")
	t.Cleanup(func() { _ = producer.Close() })

	svc := New(videorepo.New(db), storage.NewLocal(localDir), slotUploadConfig(localDir),
		config.TranscodeConfig{MaxPending: slotLimitNotBinding}, producer, rdb)
	if err := rdb.Del(ctx, transcodeSlotKey).Err(); err != nil {
		t.Fatalf("清理信号量键失败: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Del(context.Background(), transcodeSlotKey).Err() })

	md5, title := newInstantSessionFixture(t, db, ctx)

	_, err := svc.InitUpload(ctx, slotTestAdminID, instantInitRequest(md5, title))
	if err == nil {
		t.Fatal("投递转码任务失败时秒传应返回错误")
	}
	// 上限给足，闸门应当放行——被拒的话说明判定口径与「有余量」矛盾。
	var ec errcode.ErrCode
	if errors.As(err, &ec) && ec.Code == errcode.ErrServerBusy.Code {
		t.Fatalf("队列有余量时秒传不应被闸门拒绝，实际: %v", err)
	}

	// 关键断言：失败路径上配额已归还。
	if used, err := svc.slots.Used(ctx); err != nil || used != 0 {
		t.Fatalf("秒传收尾失败后应归还配额: used=%d err=%v", used, err)
	}
	if n := countVideosByTitle(t, db, title); n != 0 {
		t.Fatalf("投递失败后不应留下记录，实际 %d 条（title=%s）", n, title)
	}
}
