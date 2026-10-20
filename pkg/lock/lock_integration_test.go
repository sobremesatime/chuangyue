//go:build integration

// 分布式锁的集成测试：需要本地 Redis（地址可用 REDIS_ADDR 覆盖）。
// Redis 不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./pkg/lock/...
package lock

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newTestRedis 连本地 Redis 并返回客户端 + 一个随机的键前缀，测试结束清理键。
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

	prefix := "lock-test:" + strconv.FormatInt(time.Now().UnixNano(), 36) + ":"
	return rdb, prefix
}

// 互斥语义：同一把键，第二个持有者抢不到；前者释放后，后者才能拿到。
func TestMutex_MutualExclusion(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	key := prefix + "mutex"
	ctx := context.Background()

	m1 := New(rdb, key, time.Minute)
	m2 := New(rdb, key, time.Minute)

	ok, err := m1.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("m1 首次抢锁应成功: ok=%v err=%v", ok, err)
	}

	ok, err = m2.TryLock(ctx)
	if err != nil {
		t.Fatalf("m2 抢锁不应报错: %v", err)
	}
	if ok {
		t.Fatal("m2 在 m1 持锁期间不应抢到锁")
	}

	released, err := m1.Unlock(ctx)
	if err != nil || !released {
		t.Fatalf("m1 释放应成功: released=%v err=%v", released, err)
	}

	ok, err = m2.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("m1 释放后 m2 应抢到锁: ok=%v err=%v", ok, err)
	}
}

// 释放必须「认属主」：键被换成别人的值后，原持有者的 Unlock 不得删掉别人的锁。
func TestMutex_UnlockGuardedByToken(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	key := prefix + "guarded"
	ctx := context.Background()

	m := New(rdb, key, time.Minute)
	if ok, err := m.TryLock(ctx); err != nil || !ok {
		t.Fatalf("抢锁应成功: ok=%v err=%v", ok, err)
	}

	// 模拟：锁已过期并被另一实例抢走（键值变成别人的 token）。
	const otherOwner = "other-owner-token"
	if err := rdb.Set(ctx, key, otherOwner, time.Minute).Err(); err != nil {
		t.Fatalf("写入他人 token 失败: %v", err)
	}

	released, err := m.Unlock(ctx)
	if err != nil {
		t.Fatalf("Unlock 不应报错: %v", err)
	}
	if released {
		t.Fatal("锁已不属于自己，Unlock 不应删除")
	}

	got, err := rdb.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("读取键失败: %v", err)
	}
	if got != otherOwner {
		t.Fatalf("他人的锁被误删了: got=%q", got)
	}
}

// 租期兜底：持有者崩溃（不释放）时，锁在 TTL 到期后应能被其它实例抢到。
func TestMutex_TTLReleasesAfterExpiry(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	key := prefix + "ttl"
	ctx := context.Background()

	m1 := New(rdb, key, 100*time.Millisecond)
	if ok, err := m1.TryLock(ctx); err != nil || !ok {
		t.Fatalf("m1 抢锁应成功: ok=%v err=%v", ok, err)
	}

	m2 := New(rdb, key, time.Minute)
	if ok, _ := m2.TryLock(ctx); ok {
		t.Fatal("TTL 未到期时 m2 不应抢到锁")
	}

	time.Sleep(150 * time.Millisecond) // 等租期过期

	ok, err := m2.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("租期过期后 m2 应抢到锁: ok=%v err=%v", ok, err)
	}
}

// 未持锁就释放属于编程错误，应显式报错而不是静默成功。
func TestMutex_UnlockWithoutHold(t *testing.T) {
	rdb, prefix := newTestRedis(t)
	m := New(rdb, prefix+"never", time.Minute)

	if _, err := m.Unlock(context.Background()); err == nil {
		t.Fatal("未持锁时 Unlock 应返回错误")
	}
}
