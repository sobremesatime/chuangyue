package grpcretry

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
)

// unavailable 构造一个「瞬时故障」错误，与下游进程重启 / 实例被摘除时的表现一致。
func unavailable() error { return status.Error(codes.Unavailable, "down") }

// sequentialInvoker 依次返回 errs 中的错误，用完后沿用最后一个；返回被调用次数。
func sequentialInvoker(errs []error) (*int, grpc.UnaryInvoker) {
	calls := 0
	invoker := func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		err := errs[len(errs)-1]
		if calls < len(errs) {
			err = errs[calls]
		}
		calls++
		return err
	}
	return &calls, invoker
}

// 白名单外的方法（此处用一个写方法）绝不重试：盲目重试写操作可能造成重复副作用。
func TestNotWhitelisted_NoRetry(t *testing.T) {
	calls, invoker := sequentialInvoker([]error{unavailable()})

	err := UnaryClientInterceptor()(context.Background(),
		userv1.UserService_UpdateProfile_FullMethodName, nil, nil, nil, invoker)

	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
	if *calls != 1 {
		t.Fatalf("写方法不应重试，期望调用 1 次，实际 %d 次", *calls)
	}
}

// 白名单内的读方法遇瞬时故障会重试，并在下游恢复时返回成功。
func TestUnavailable_RetriesThenSucceeds(t *testing.T) {
	calls, invoker := sequentialInvoker([]error{unavailable(), nil})

	err := UnaryClientInterceptor()(context.Background(),
		userv1.UserService_GetUsers_FullMethodName, nil, nil, nil, invoker)

	if err != nil {
		t.Fatalf("重试后应成功，实际 err=%v", err)
	}
	if *calls != 2 {
		t.Fatalf("期望调用 2 次（1 次失败 + 1 次成功），实际 %d 次", *calls)
	}
}

// 非瞬时故障立即返回，不浪费调用方的时间预算。
func TestNonUnavailable_ReturnsImmediately(t *testing.T) {
	calls, invoker := sequentialInvoker([]error{status.Error(codes.NotFound, "nope")})

	err := UnaryClientInterceptor()(context.Background(),
		userv1.UserService_GetUsers_FullMethodName, nil, nil, nil, invoker)

	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if *calls != 1 {
		t.Fatalf("非 Unavailable 不应重试，期望调用 1 次，实际 %d 次", *calls)
	}
}

// 一直不可用时最多尝试 maxAttempts 次，随后放弃并返回最后一次错误。
func TestExhaustsAttempts(t *testing.T) {
	calls, invoker := sequentialInvoker([]error{unavailable()})

	start := time.Now()
	err := UnaryClientInterceptor()(context.Background(),
		userv1.UserService_GetUsers_FullMethodName, nil, nil, nil, invoker)
	elapsed := time.Since(start)

	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
	if *calls != maxAttempts {
		t.Fatalf("期望尝试 %d 次，实际 %d 次", maxAttempts, *calls)
	}
	// 两次退避：initialBackoff + 2*initialBackoff = 300ms，留出余量后断言下限。
	if min := 250 * time.Millisecond; elapsed < min {
		t.Fatalf("退避总耗时过短（%v < %v），可能未真正等待", elapsed, min)
	}
}

// 退避等待响应 context 取消：调用方超时后不再空等到下一次尝试。
func TestContextCanceledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	calls, invoker := sequentialInvoker([]error{unavailable()})

	start := time.Now()
	_ = UnaryClientInterceptor()(ctx,
		userv1.UserService_GetUsers_FullMethodName, nil, nil, nil, invoker)
	elapsed := time.Since(start)

	if *calls != 1 {
		t.Fatalf("取消后不应再发起尝试，期望调用 1 次，实际 %d 次", *calls)
	}
	if elapsed >= initialBackoff {
		t.Fatalf("ctx 取消后应立即返回，实际耗时 %v（>= 退避 %v）", elapsed, initialBackoff)
	}
}

// 重试前清空上一轮的响应：protobuf 解组是合并语义，不清空会把两次结果叠加。
func TestReplyResetBetweenAttempts(t *testing.T) {
	calls := 0
	invoker := func(_ context.Context, _ string, _, reply any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		calls++
		resp := reply.(*userv1.GetUsersResponse)
		resp.Users = append(resp.Users, &userv1.UserBrief{Id: int64(calls)})
		if calls == 1 {
			return unavailable()
		}
		return nil
	}

	reply := &userv1.GetUsersResponse{}
	err := UnaryClientInterceptor()(context.Background(),
		userv1.UserService_GetUsers_FullMethodName, nil, reply, nil, invoker)

	if err != nil {
		t.Fatalf("重试后应成功，实际 err=%v", err)
	}
	if len(reply.Users) != 1 || reply.Users[0].Id != 2 {
		t.Fatalf("reply 未在重试前清空，实际 users=%+v", reply.Users)
	}
}
