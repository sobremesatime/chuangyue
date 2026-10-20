package grpcclient

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// TestUnaryTimeoutInterceptor 验证超时拦截器的两条口径：
// 没有 deadline 时补默认值；已有 deadline 时沿用调用方的设定、不擅自改口径。
func TestUnaryTimeoutInterceptor(t *testing.T) {
	const timeout = 50 * time.Millisecond
	interceptor := unaryTimeoutInterceptor(timeout)

	// 记录 invoker 实际收到的 context，供断言使用。
	var got context.Context
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		got = ctx
		return nil
	}

	t.Run("no_deadline_gets_default", func(t *testing.T) {
		if err := interceptor(context.Background(), "/demo/Method", nil, nil, nil, invoker); err != nil {
			t.Fatalf("拦截器不应返回错误: %v", err)
		}

		deadline, ok := got.Deadline()
		if !ok {
			t.Fatal("未设 deadline 的调用应被补上超时")
		}
		// 允许少量调度误差，只断言落在 (0, timeout] 区间内。
		if left := time.Until(deadline); left <= 0 || left > timeout {
			t.Fatalf("补上的超时应为 %v 以内且未过期, 实际剩余 %v", timeout, left)
		}
	})

	t.Run("existing_deadline_is_kept", func(t *testing.T) {
		const long = 10 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), long)
		defer cancel()

		if err := interceptor(ctx, "/demo/Method", nil, nil, nil, invoker); err != nil {
			t.Fatalf("拦截器不应返回错误: %v", err)
		}

		deadline, ok := got.Deadline()
		if !ok {
			t.Fatal("调用方设置的 deadline 应被保留")
		}
		// 被覆盖成默认值的话剩余时间会掉到 50ms 以内，这里应仍接近 long。
		if left := time.Until(deadline); left <= timeout {
			t.Fatalf("调用方的 deadline 不应被覆盖, 实际剩余 %v", left)
		}
	})
}
