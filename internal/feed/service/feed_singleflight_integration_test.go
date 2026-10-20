//go:build integration

// 首页单飞的集成测试：需要本地 Redis（地址可用 REDIS_ADDR 覆盖，默认 127.0.0.1:6379）。
// Redis 不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/feed/service/...
//
// 下游用桩客户端替代，把「回源被调了几次」变成可断言的数字——这是「缓存未命中时
// 并发请求只回源一次」的直接证据：改造前这 32 个并发请求会各跨一次 video + interaction。
package service

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
)

// newFeedTestRedis 连本地 Redis（REDIS_ADDR 可覆盖），连不上直接跳过。
func newFeedTestRedis(t *testing.T) *redis.Client {
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

// countingVideoClient 是只实现 ListFeedCandidates 的桩：内嵌（nil 的）接口让其余 RPC 在
// 编译期就满足，真被调到会 panic——本用例的路径只该调这一个方法，多调了就是 bug。
type countingVideoClient struct {
	videov1.VideoServiceClient
	calls atomic.Int64
	// delay 拉长「回源重建」的耗时，把并发请求都挤在同一个未命中窗口里；
	// 没有它，leader 可能在其它请求进来之前就写完缓存，用例就测不到单飞。
	delay time.Duration
}

func (c *countingVideoClient) ListFeedCandidates(_ context.Context, _ *videov1.ListFeedCandidatesRequest, _ ...grpc.CallOption) (*videov1.ListFeedCandidatesResponse, error) {
	c.calls.Add(1)
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	// 候选池比一页多一些即可，内容不重要——要断言的是「被调了几次」，不是推荐质量。
	return &videov1.ListFeedCandidatesResponse{Videos: []*videov1.VideoBrief{
		{Id: 1, Title: "sf-1", CategoryId: 11, ViewCount: 300},
		{Id: 2, Title: "sf-2", CategoryId: 12, ViewCount: 200},
		{Id: 3, Title: "sf-3", CategoryId: 13, ViewCount: 100},
	}}, nil
}

// countingInteractionClient 是只实现 GetUserSignals 的桩：返回空信号（不看任何视频），
// 于是个性化重排只走「拉候选池」这一步，不存在兴趣分类查询，断言的口径最干净。
type countingInteractionClient struct {
	interactionv1.InteractionServiceClient
	calls atomic.Int64
	delay time.Duration
}

func (c *countingInteractionClient) GetUserSignals(_ context.Context, _ *interactionv1.GetUserSignalsRequest, _ ...grpc.CallOption) (*interactionv1.GetUserSignalsResponse, error) {
	c.calls.Add(1)
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return &interactionv1.GetUserSignalsResponse{}, nil
}

// TestHome_SingleFlightOnCacheMiss 是本次改造的核心用例：同一个冷缓存键上 N 个并发请求，
// 下游只应被回源一次。
func TestHome_SingleFlightOnCacheMiss(t *testing.T) {
	rdb := newFeedTestRedis(t)
	ctx := context.Background()

	video := &countingVideoClient{delay: 150 * time.Millisecond}
	interaction := &countingInteractionClient{delay: 150 * time.Millisecond}
	svc := New(video, interaction, rdb)

	// 用**唯一的登录用户**当缓存键：feed:home:u:<userID> 天然按用户隔离，
	// 不会与其它用例、也不会与本机正在运行的 feed 实例（每 1min 预热「未登录」榜单的
	// 周期任务）抢同一把键。未登录那条键是全局固定的，不适合在共享 Redis 上做断言。
	userID := time.Now().UnixNano()
	cacheKey := fmt.Sprintf("%s%d", userCacheKeyPrefix, userID)
	lockKey := fmt.Sprintf("%s%d", userLockKeyPrefix, userID)
	t.Cleanup(func() { rdb.Del(context.Background(), cacheKey, lockKey) })

	const goroutines = 32
	var (
		wg     sync.WaitGroup
		start  = make(chan struct{})
		errs   = make(chan error, goroutines)
		totals = make([]int64, goroutines)
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 让所有请求尽量同一瞬间发起，最大化对冷缓存的竞争
			_, total, err := svc.Home(ctx, userID, 1, 20)
			if err != nil {
				errs <- err
				return
			}
			totals[i] = total
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("并发首页请求不应报错: %v", err)
	}

	// 核心断言：一次冷缓存的击穿窗口里，候选池只被拉了一次。
	if got := video.calls.Load(); got != 1 {
		t.Fatalf("%d 个并发首页请求只应触发 1 次候选池 RPC（单飞未生效），实际 %d 次", goroutines, got)
	}
	if got := interaction.calls.Load(); got != 1 {
		t.Fatalf("用户信号 RPC 同样只应 1 次，实际 %d 次", got)
	}
	// 等待者拿到的是 leader 算出来的结果，而不是空列表（否则单飞等于把请求做废了）。
	for i, total := range totals {
		if total <= 0 {
			t.Fatalf("第 %d 个请求拿到的结果为空（total=%d），说明等待者没复用 leader 的结果", i, total)
		}
	}

	// 重建正常收尾：锁已释放、结果已落缓存。
	if n, err := rdb.Exists(ctx, lockKey).Result(); err != nil || n != 0 {
		t.Fatalf("重建结束后单飞锁应已释放: exists=%d err=%v", n, err)
	}
	if n, err := rdb.Exists(ctx, cacheKey).Result(); err != nil || n != 1 {
		t.Fatalf("重建结果应已写入缓存: exists=%d err=%v", n, err)
	}

	// 缓存命中后的请求走快路径，不该再产生任何回源。
	before := video.calls.Load()
	if _, total, err := svc.Home(ctx, userID, 1, 20); err != nil || total <= 0 {
		t.Fatalf("缓存命中时请求应正常返回: total=%d err=%v", total, err)
	}
	if after := video.calls.Load(); after != before {
		t.Fatalf("缓存命中不应再回源，实际又多回了 %d 次", after-before)
	}
}
