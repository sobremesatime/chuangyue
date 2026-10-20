//go:build integration

// 限流器的集成测试：需要本地 Redis（地址可用 REDIS_ADDR 覆盖）。
// Redis 不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./pkg/ratelimit/...
package ratelimit

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newTestLimiter 连本地 Redis 并返回一个带随机前缀的限流器，测试结束清理键。
func newTestLimiter(t *testing.T, capacity int, perSecond float64) (*redis.Client, *Limiter) {
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

	prefix := "rl-test:" + strconv.FormatInt(time.Now().UnixNano(), 36) + ":"
	return rdb, New(rdb, prefix, capacity, perSecond)
}

// 桶容量即突发额度：容量 3 时前 3 次放行，第 4 次被拒。
func TestLimiter_BurstThenReject(t *testing.T) {
	_, l := newTestLimiter(t, 3, 0.001) // 补充速率极低，测试期间等价于不补充
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		res, err := l.Allow(ctx, "k")
		if err != nil {
			t.Fatalf("第 %d 次判定失败: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("第 %d 次应放行（桶容量 3）", i)
		}
	}

	res, err := l.Allow(ctx, "k")
	if err != nil {
		t.Fatalf("第 4 次判定失败: %v", err)
	}
	if res.Allowed {
		t.Fatal("第 4 次应被拒绝（超过桶容量）")
	}
	if res.Remaining != 0 {
		t.Fatalf("被拒时剩余应为 0，实际 %d", res.Remaining)
	}
}

// 不同 key 各自独立计数。
func TestLimiter_KeysAreIsolated(t *testing.T) {
	_, l := newTestLimiter(t, 1, 0.001)
	ctx := context.Background()

	if res, _ := l.Allow(ctx, "a"); !res.Allowed {
		t.Fatal("key a 首次应放行")
	}
	if res, _ := l.Allow(ctx, "a"); res.Allowed {
		t.Fatal("key a 第二次应被拒")
	}
	if res, _ := l.Allow(ctx, "b"); !res.Allowed {
		t.Fatal("key b 首次应放行（与 a 互不影响）")
	}
}

// 令牌会随时间按速率补充。
func TestLimiter_RefillsOverTime(t *testing.T) {
	_, l := newTestLimiter(t, 1, 100) // 100 个/秒 → 10ms 补 1 个
	ctx := context.Background()

	if res, _ := l.Allow(ctx, "k"); !res.Allowed {
		t.Fatal("首次应放行")
	}
	if res, _ := l.Allow(ctx, "k"); res.Allowed {
		t.Fatal("紧接着的第二次应被拒（令牌已耗尽）")
	}

	time.Sleep(30 * time.Millisecond) // 足够补回 1 个以上令牌

	if res, _ := l.Allow(ctx, "k"); !res.Allowed {
		t.Fatal("等待补充后应再次放行")
	}
}
