//go:build integration

// 播放量「Redis 缓冲 + 批量落库」的集成测试：需要本地 MySQL 与 Redis，任一不可用则跳过。
//
//	go test -tags integration -v -run TestViewCountBuffer ./internal/video/handler/...
//
// 注意：测试直接操作视频服务共用的缓冲键，运行前会清空它，因此只应在本地开发栈上跑。
package handler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
)

// viewCountDeltaKey 与 service 内部的缓冲键一致，用于测试前置清理与后置断言。
const viewCountDeltaKey = "video:view:delta"

// newViewCountRedis 连本地 Redis（可用 REDIS_ADDR 覆盖），不可用则跳过。
func newViewCountRedis(t *testing.T) *redis.Client {
	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
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

// viewCount 读取指定视频当前的播放量。
func viewCount(t *testing.T, db *gorm.DB, id int64) int64 {
	t.Helper()

	var v videomodel.Video
	if err := db.Select("view_count").First(&v, id).Error; err != nil {
		t.Fatalf("读取视频播放量失败: %v", err)
	}
	return v.ViewCount
}

// TestViewCountBufferFlush 覆盖完整链路：累加只进缓冲（读路径不写库）→ 批量落库一次写入
// → 缓冲清空 → 空缓冲再落库不重复累加。
func TestViewCountBufferFlush(t *testing.T) {
	db := newTestDB(t)
	rdb := newViewCountRedis(t)
	ctx := context.Background()

	pub := publishedVideo(t, db)
	if pub == nil {
		t.Skip("库中没有已上架视频，跳过播放量缓冲验证")
	}

	svc := service.New(videorepo.New(db), nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, rdb)

	// 清空缓冲，只保留本测试写入的增量，避免残留数据干扰断言。
	if err := rdb.Del(ctx, viewCountDeltaKey).Err(); err != nil {
		t.Fatalf("清理播放量缓冲失败: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Del(context.Background(), viewCountDeltaKey).Err() })

	before := viewCount(t, db, pub.ID)

	const incrs = 3
	for i := 0; i < incrs; i++ {
		if err := svc.IncrViewCount(ctx, pub.ID); err != nil {
			t.Fatalf("第 %d 次累加播放量失败: %v", i+1, err)
		}
	}

	// 关键断言：累加只写进 Redis 缓冲，库中计数不动——这正是「读路径不再同步写库」的证明。
	if got := viewCount(t, db, pub.ID); got != before {
		t.Fatalf("累加后库中播放量不应变化（应只落缓冲）: got=%d want=%d", got, before)
	}
	if rdb.Exists(ctx, viewCountDeltaKey).Val() == 0 {
		t.Fatal("累加后缓冲键应存在")
	}

	// 落库：把缓冲里的增量一次性批量写回。
	svc.FlushViewCounts(ctx)
	if got, want := viewCount(t, db, pub.ID), before+incrs; got != want {
		t.Fatalf("批量落库后播放量应为 %d: got=%d", want, got)
	}
	if rdb.Exists(ctx, viewCountDeltaKey).Val() != 0 {
		t.Fatal("落库后缓冲键应被清空")
	}

	// 幂等：缓冲已空，再跑一轮不应重复累加。
	svc.FlushViewCounts(ctx)
	if got, want := viewCount(t, db, pub.ID), before+incrs; got != want {
		t.Fatalf("空缓冲再落库不应改变数据: got=%d want=%d", got, want)
	}
}

// TestViewCountFlushBatchUpdate 覆盖批量 SQL 本身：一批多个视频各带不同增量时，
// 每条只加自己的增量（验证 CASE WHEN 的行映射与 WHERE 收敛范围都正确）。
func TestViewCountFlushBatchUpdate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var vids []videomodel.Video
	if err := db.Where("status = ?", videomodel.StatusPublished).Order("id ASC").Limit(2).Find(&vids).Error; err != nil {
		t.Fatalf("查询已上架视频失败: %v", err)
	}
	if len(vids) < 2 {
		t.Skip("库中已上架视频不足 2 条，跳过批量更新验证")
	}

	repo := videorepo.New(db)
	idA, idB := vids[0].ID, vids[1].ID
	beforeA := viewCount(t, db, idA)
	beforeB := viewCount(t, db, idB)

	// 两条视频给不同增量：若 SQL 把 CASE 映射错了行，或漏了 WHERE 把别的行也刷了，
	// 这里立刻能看出来。
	if err := repo.IncrViewCounts(ctx, map[int64]int64{idA: 5, idB: 2}); err != nil {
		t.Fatalf("批量累加播放量失败: %v", err)
	}
	if got, want := viewCount(t, db, idA), beforeA+5; got != want {
		t.Fatalf("视频 %d 播放量应为 %d: got=%d", idA, want, got)
	}
	if got, want := viewCount(t, db, idB), beforeB+2; got != want {
		t.Fatalf("视频 %d 播放量应为 %d: got=%d", idB, want, got)
	}

	// 空 map 是合法输入（缓冲里没东西可落时的情形），不应报错也不应改动数据。
	if err := repo.IncrViewCounts(ctx, nil); err != nil {
		t.Fatalf("空增量集合不应报错: %v", err)
	}
	if got := viewCount(t, db, idA); got != beforeA+5 {
		t.Fatalf("空增量集合不应改动数据: got=%d want=%d", got, beforeA+5)
	}
}
