//go:build integration

// 计数信号量的集成测试：需要本地 Redis（地址可用 REDIS_ADDR 覆盖）。
// Redis 不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./pkg/semaphore/...
package semaphore

import (
	"context"
	"os"
	"strconv"
	"sync"
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

	prefix := "semaphore-test:" + strconv.FormatInt(time.Now().UnixNano(), 36) + ":"
	return rdb, prefix
}

// 上限语义：取满之后再取就失败，且失败方不会污染计数。
func TestSemaphore_RespectsLimit(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	sem := New(rdb, prefix+"limit", 2, time.Minute)

	for i := 0; i < 2; i++ {
		if _, ok, err := sem.TryAcquire(ctx, 0); err != nil || !ok {
			t.Fatalf("第 %d 次预约应成功: ok=%v err=%v", i+1, ok, err)
		}
	}

	if _, ok, err := sem.TryAcquire(ctx, 0); err != nil {
		t.Fatalf("取满后不应报错: %v", err)
	} else if ok {
		t.Fatal("上限 2 时第 3 次预约不应成功")
	}

	if used, err := sem.Used(ctx); err != nil || used != 2 {
		t.Fatalf("占用应为 2: used=%d err=%v", used, err)
	}
}

// 归还后余量应立即可被他人使用。
func TestSemaphore_ReleaseFreesSlot(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	sem := New(rdb, prefix+"release", 1, time.Minute)

	lease, ok, err := sem.TryAcquire(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("首次预约应成功: ok=%v err=%v", ok, err)
	}

	if _, ok, _ := sem.TryAcquire(ctx, 0); ok {
		t.Fatal("未归还时不应再取到")
	}

	released, err := lease.Release(ctx)
	if err != nil || !released {
		t.Fatalf("归还应成功: released=%v err=%v", released, err)
	}

	if _, ok, err := sem.TryAcquire(ctx, 0); err != nil || !ok {
		t.Fatalf("归还后应能再取到: ok=%v err=%v", ok, err)
	}
	if used, _ := sem.Used(ctx); used != 1 {
		t.Fatalf("归还旧预约、新增一次后占用应为 1: used=%d", used)
	}
}

// extra 参与余量计算：被数在信号量之外的占用（如库里已在转码的记录）也要扣掉。
func TestSemaphore_ExtraCountsTowardLimit(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	sem := New(rdb, prefix+"extra", 3, time.Minute)

	// 上限 3，外部已占 2，只剩 1 个余量。
	if _, ok, err := sem.TryAcquire(ctx, 2); err != nil || !ok {
		t.Fatalf("外部占 2 时仍应有 1 个余量: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := sem.TryAcquire(ctx, 2); ok {
		t.Fatal("外部占 2 + 已预约 1 = 3，不应再取到")
	}
}

// 租期兜底：持有者崩溃（不归还）时，预约在租期到期后被下一次 TryAcquire 回收。
//
// 这是「信号量不会因进程崩溃而永久缩容」的保证——没有它，上限会被幽灵占用一点点吃掉。
func TestSemaphore_ReclaimsExpiredLease(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	sem := New(rdb, prefix+"lease", 1, 100*time.Millisecond)

	leaked, ok, err := sem.TryAcquire(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("首次预约应成功: ok=%v err=%v", ok, err)
	}
	_ = leaked // 故意不归还，模拟持有者崩溃

	if _, ok, _ := sem.TryAcquire(ctx, 0); ok {
		t.Fatal("租期未到时不应回收")
	}

	time.Sleep(150 * time.Millisecond) // 等租期过期

	if _, ok, err := sem.TryAcquire(ctx, 0); err != nil || !ok {
		t.Fatalf("租期过期后泄漏的预约应被回收: ok=%v err=%v", ok, err)
	}
}

// 释放只动自己那一条：不能因为释放而把别人的预约一起删掉。
func TestSemaphore_ReleaseOnlyOwnLease(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	sem := New(rdb, prefix+"own", 2, time.Minute)

	first, _, err := sem.TryAcquire(ctx, 0)
	if err != nil {
		t.Fatalf("首次预约失败: %v", err)
	}
	if _, _, err := sem.TryAcquire(ctx, 0); err != nil {
		t.Fatalf("第二次预约失败: %v", err)
	}

	if released, err := first.Release(ctx); err != nil || !released {
		t.Fatalf("释放自己的预约应成功: released=%v err=%v", released, err)
	}
	if used, _ := sem.Used(ctx); used != 1 {
		t.Fatalf("只应释放自己那一条，占用应为 1: used=%d", used)
	}

	// 重复释放属于编程错误：与 pkg/lock 的「未持锁就 Unlock」同口径，显式报错而不静默成功。
	if _, err := first.Release(ctx); err == nil {
		t.Fatal("重复释放应返回错误")
	}
}

// 并发抢配额**不会超卖**：这是 TOCTOU 被修掉的核心证据。
//
// 旧实现是「读一次库计数 → 判断 → 继续」，并发收尾的 N 个上传会全部读到同一份余量；
// 这里把判断与占位放进一段 Lua，成功数必须恰好等于余量。
func TestSemaphore_ConcurrentAcquireNeverOversells(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()

	const limit = 5
	const goroutines = 32

	sem := New(rdb, prefix+"concurrent", limit, time.Minute)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		leases    []*Lease
	)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 让所有 goroutine 尽量同一瞬间发起，最大化竞争
			lease, ok, err := sem.TryAcquire(ctx, 0)
			if err != nil {
				t.Errorf("并发预约不应报错: %v", err)
				return
			}
			if !ok {
				return
			}
			mu.Lock()
			succeeded++
			leases = append(leases, lease)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if succeeded != limit {
		t.Fatalf("并发下成功数应恰好等于上限 %d，实际 %d（超出即为超卖）", limit, succeeded)
	}
	if used, _ := sem.Used(ctx); used != limit {
		t.Fatalf("记录到的占用应等于上限: used=%d", used)
	}
}

// limit <= 0 表示不限制：永远成功，且归还空预约不报错。
func TestSemaphore_UnlimitedWhenLimitNonPositive(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	ctx := context.Background()
	sem := New(rdb, prefix+"unlimited", 0, time.Minute)

	lease, ok, err := sem.TryAcquire(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("不限制时应总是成功: ok=%v err=%v", ok, err)
	}
	if released, err := lease.Release(ctx); err != nil || released {
		t.Fatalf("空预约归还应是空操作: released=%v err=%v", released, err)
	}
	// 不限制时不应在 Redis 留下任何记录。
	if used, _ := sem.Used(ctx); used != 0 {
		t.Fatalf("不限制时不应产生预约记录: used=%d", used)
	}
}
