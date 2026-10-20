//go:build integration

// 单飞的集成测试：需要本地 Redis（地址可用 REDIS_ADDR 覆盖）。
// Redis 不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./pkg/singleflight/...
//
// 断言的口径是「build 被调了几次」——单飞的全部价值就是把这个数字从 N（甚至 N×M）压到 1。
// 进程内合并与跨实例选主各有用例，另有两个「等不到就自己算」的兜底用例：
// 单飞的边界在于它绝不能把请求挂住，因此失败路径同样要被测试。
package singleflight

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newTestRedis 连本地 Redis 并返回客户端 + 一个随机的键前缀，避免与其它测试 / 运行实例互相干扰。
func newTestRedis(t *testing.T) (*redis.Client, string) {
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

	return rdb, "singleflight-test:" + strconv.FormatInt(time.Now().UnixNano(), 36) + ":"
}

// loadOf 造一个「从 Redis 读」的 load；取不到（未命中或出错）都算未命中。
func loadOf(rdb *redis.Client, key string) func(context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) {
		v, err := rdb.Get(ctx, key).Result()
		if err != nil {
			return "", false
		}
		return v, true
	}
}

// storeValue 是 build 里「写回结果」的那一步：单飞的约定是 build 返回前 load 必须能看见它。
func storeValue(ctx context.Context, rdb *redis.Client, key, val string) error {
	return rdb.Set(ctx, key, val, time.Minute).Err()
}

// 缓存命中时 load 直接返回：不执行 build，也根本不去碰锁。
func TestFlight_CacheHitSkipsBuildAndLock(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	cacheKey, lockKey := prefix+"hit:cache", prefix+"hit:lock"

	if err := storeValue(ctx, rdb, cacheKey, "cached"); err != nil {
		t.Fatalf("预置缓存失败: %v", err)
	}

	f := New[string](rdb, 3*time.Second, 500*time.Millisecond, 20*time.Millisecond)
	var builds atomic.Int64

	got, err := f.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(context.Context) (string, error) {
		builds.Add(1)
		return "rebuilt", nil
	})
	if err != nil {
		t.Fatalf("命中缓存不应报错: %v", err)
	}
	if got != "cached" {
		t.Fatalf("应返回缓存里的值，实际 %q", got)
	}
	if builds.Load() != 0 {
		t.Fatalf("命中缓存不应触发重建，实际重建 %d 次", builds.Load())
	}
	// 快路径连锁都不该申请，Redis 里不该留下锁键。
	if n, _ := rdb.Exists(ctx, lockKey).Result(); n != 0 {
		t.Fatalf("快路径不应产生单飞锁: exists=%d", n)
	}
}

// 进程内合并：同一实例内 N 个并发 Do 只执行一次 build，其余共享它的结果。
//
// 这是「缓存击穿」在单实例上的那一半：少了它，一个实例内 32 个并发请求会变成
// 1 个 leader + 31 个 follower 等超时后各自回源。
func TestFlight_MergesConcurrentCallsInProcess(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	cacheKey, lockKey := prefix+"merge:cache", prefix+"merge:lock"

	f := New[string](rdb, 3*time.Second, 500*time.Millisecond, 20*time.Millisecond)
	var builds atomic.Int64

	const goroutines = 32
	var (
		wg     sync.WaitGroup
		start  = make(chan struct{})
		values = make([]string, goroutines)
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := f.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(ctx context.Context) (string, error) {
				builds.Add(1)
				// 拉长重建耗时，保证后来者一定撞上「正在重建」这个窗口。
				time.Sleep(100 * time.Millisecond)
				if err := storeValue(ctx, rdb, cacheKey, "leader-result"); err != nil {
					return "", err
				}
				return "leader-result", nil
			})
			if err != nil {
				t.Errorf("并发调用不应报错: %v", err)
				return
			}
			values[i] = v
		}(i)
	}
	close(start)
	wg.Wait()

	if got := builds.Load(); got != 1 {
		t.Fatalf("%d 个并发只应重建 1 次，实际 %d 次", goroutines, got)
	}
	for i, v := range values {
		if v != "leader-result" {
			t.Fatalf("第 %d 个调用者拿到了 %q，应与 leader 的结果一致", i, v)
		}
	}
	if n, err := rdb.Exists(ctx, lockKey).Result(); err != nil || n != 0 {
		t.Fatalf("leader 正常收尾后锁应已释放: exists=%d err=%v", n, err)
	}
}

// 跨实例选主：两个独立的 Flight 实例（模型上就是两个副本进程，进程内的合并表各自私有）
// 面对同一个冷键时，只有抢到锁的那个回源，另一个等它的结果。
//
// 这是单实例用例覆盖不到的那一半：少了两实例之间的这层，M 个副本会各算一遍。
func TestFlight_CrossInstanceOnlyLeaderBuilds(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	cacheKey, lockKey := prefix+"cross:cache", prefix+"cross:lock"

	leader := New[string](rdb, 3*time.Second, 2*time.Second, 20*time.Millisecond)
	follower := New[string](rdb, 3*time.Second, 2*time.Second, 20*time.Millisecond)

	var leaderBuilds, followerBuilds atomic.Int64
	leaderBuilding := make(chan struct{})
	release := make(chan struct{})

	go func() {
		_, _ = leader.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(ctx context.Context) (string, error) {
			leaderBuilds.Add(1)
			close(leaderBuilding) // 通知主 goroutine：已抢到锁并开始重建
			<-release             // 卡住，制造出「重建正在进行中」的窗口
			if err := storeValue(ctx, rdb, cacheKey, "leader-result"); err != nil {
				return "", err
			}
			return "leader-result", nil
		})
	}()

	// 让 follower 先进入等待，过一会儿 leader 才写结果——follower 应当从缓存里捡到它。
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(release)
	}()

	<-leaderBuilding

	got, err := follower.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(context.Context) (string, error) {
		followerBuilds.Add(1)
		return "follower-result", nil
	})
	if err != nil {
		t.Fatalf("follower 不应报错: %v", err)
	}
	if got != "leader-result" {
		t.Fatalf("follower 应复用 leader 写下的结果，实际 %q（说明它自己也回源了）", got)
	}
	if n := leaderBuilds.Load(); n != 1 {
		t.Fatalf("leader 应恰好重建 1 次，实际 %d 次", n)
	}
	if n := followerBuilds.Load(); n != 0 {
		t.Fatalf("抢不到锁的实例不应回源，实际回源 %d 次", n)
	}
	if n, _ := rdb.Exists(ctx, lockKey).Result(); n != 0 {
		t.Fatalf("结束后锁应已释放: exists=%d", n)
	}
}

// 等待有上限：leader 的重建比 follower 的耐心还长（且一直不写结果）时，
// follower 必须自己兜底回源，而不是无限期等下去。
//
// 这是本机制「可用性不押在 leader 身上」的核心证据。
func TestFlight_FollowerFallsBackAfterWaitFor(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	cacheKey, lockKey := prefix+"wait:cache", prefix+"wait:lock"

	leader := New[string](rdb, 5*time.Second, 20*time.Millisecond, 5*time.Millisecond)
	follower := New[string](rdb, 5*time.Second, 150*time.Millisecond, 20*time.Millisecond)

	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) }) // 用例结束时放行卡死的 leader，避免 goroutine 留到进程退出
	leaderBuilding := make(chan struct{})

	go func() {
		_, _ = leader.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(context.Context) (string, error) {
			close(leaderBuilding)
			<-stuck // 只占着锁，永远不写结果
			return "", errors.New("leader 卡死，未产出结果")
		})
	}()
	<-leaderBuilding

	start := time.Now()
	var builds atomic.Int64
	got, err := follower.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(context.Context) (string, error) {
		builds.Add(1)
		return "follower-result", nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("等不到结果时应自行兜底，不应报错: %v", err)
	}
	if got != "follower-result" {
		t.Fatalf("应返回兜底自算的结果，实际 %q", got)
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("兜底回源应恰好 1 次，实际 %d 次", n)
	}
	// 必须先等满 waitFor 才兜底：既证明「确实等过」（而不是一抢不到就重复回源），
	// 也钉住等待上限本身。
	if elapsed < 150*time.Millisecond {
		t.Fatalf("应先等满 waitFor(150ms) 再兜底，实际只等了 %v", elapsed)
	}
}

// 等待期间 ctx 被取消（客户端断开 / 上游超时）：立刻带着取消原因退出，且不再回源。
func TestFlight_FollowerRespectsContextCancel(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	cacheKey, lockKey := prefix+"cancel:cache", prefix+"cancel:lock"

	leader := New[string](rdb, 5*time.Second, 20*time.Millisecond, 5*time.Millisecond)
	follower := New[string](rdb, 5*time.Second, 5*time.Second, 20*time.Millisecond)

	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	leaderBuilding := make(chan struct{})

	go func() {
		_, _ = leader.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(context.Context) (string, error) {
			close(leaderBuilding)
			<-stuck
			return "", errors.New("leader 卡死，未产出结果")
		})
	}()
	<-leaderBuilding

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()

	var builds atomic.Int64
	_, err := follower.Do(waitCtx, lockKey, loadOf(rdb, cacheKey), func(context.Context) (string, error) {
		builds.Add(1)
		return "follower-result", nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待期间 ctx 超时应把取消原因带出来，实际 %v", err)
	}
	if n := builds.Load(); n != 0 {
		t.Fatalf("ctx 已取消就不该再回源，实际回源 %d 次", n)
	}
}

// Redis 不可用：直接自算（fail-open），且**不进入等待**。
//
// 这里若去等，每个请求都要白等一个 waitFor 而结果必然等不到——load 读的也是同一份 Redis。
func TestFlight_FallsBackWithoutRedis(t *testing.T) {
	rdb, _ := newTestRedis(t)
	_ = rdb.Close() // 关掉的客户端：任何命令立刻返回「client is closed」

	f := New[string](rdb, 3*time.Second, 500*time.Millisecond, 20*time.Millisecond)

	start := time.Now()
	got, err := f.Do(context.Background(), "closed-lock",
		func(context.Context) (string, bool) { return "", false },
		func(context.Context) (string, error) { return "self-built", nil })
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Redis 不可用时应 fail-open 自算: %v", err)
	}
	if got != "self-built" {
		t.Fatalf("应返回自算结果，实际 %q", got)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("Redis 故障时不应进入等待，实际耗时 %v", elapsed)
	}
}

// 租期兜底：leader 崩溃（被 kill -9，既不写结果也不释放锁）后，锁在租期到期时自动失效，
// 新实例能立刻接手——不需要任何人工清理。
//
// 判定方式用「接手耗时」区分两条路径：抢到锁是立刻回源，抢不到才会等满 waitFor。
func TestFlight_LockExpiresAfterLeaderCrash(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	cacheKey, lockKey := prefix+"ttl:cache", prefix+"ttl:lock"

	crashed := New[string](rdb, 200*time.Millisecond, time.Second, 20*time.Millisecond)
	dead := make(chan struct{})
	t.Cleanup(func() { close(dead) })
	crashedBuilding := make(chan struct{})

	go func() {
		_, _ = crashed.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(context.Context) (string, error) {
			close(crashedBuilding)
			<-dead // 永不返回：模拟崩溃在重建中的 leader
			return "", errors.New("崩溃的 leader")
		})
	}()
	<-crashedBuilding

	// 等过租期：锁不做续期，到期即失效，无需任何人工清理。
	time.Sleep(250 * time.Millisecond)

	recovered := New[string](rdb, 200*time.Millisecond, 2*time.Second, 20*time.Millisecond)
	start := time.Now()
	got, err := recovered.Do(ctx, lockKey, loadOf(rdb, cacheKey), func(ctx context.Context) (string, error) {
		if err := storeValue(ctx, rdb, cacheKey, "recovered"); err != nil {
			return "", err
		}
		return "recovered", nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("租期到期后应能接手: %v", err)
	}
	if got != "recovered" {
		t.Fatalf("应返回接手后自算的结果，实际 %q", got)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("租期到期后应立刻抢到锁接手，而不是等满 waitFor 再兜底（实际耗时 %v）", elapsed)
	}
}
