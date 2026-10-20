package circuitbreaker

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
)

// fakeClock 是可手动推进的时钟，让熔断的冷却期在测试里即刻流逝。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// unavailable 构造一个「下游不可用」错误，属于应被计入的可用性故障。
func unavailable() error { return status.Error(codes.Unavailable, "down") }

// alwaysInvoker 返回固定错误（nil 表示成功），并统计被调用次数。
func alwaysInvoker(err error) (*int, grpc.UnaryInvoker) {
	calls := 0
	return &calls, func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		calls++
		return err
	}
}

func newTestBreaker(threshold int, clock *fakeClock) *Breaker {
	return NewBreaker(Options{
		FailureThreshold: threshold,
		OpenTimeout:      50 * time.Millisecond,
		HalfOpenMax:      1,
		Now:              clock.now,
	})
}

// 连续失败达到阈值后打开，随后的调用被直接拒绝且不再触达下游。
func TestBreaker_OpensAfterThresholdAndFastFails(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b := newTestBreaker(3, clock)
	calls, invoker := alwaysInvoker(unavailable())
	it := b.UnaryClientInterceptor()

	for i := 0; i < 3; i++ {
		if err := it(context.Background(), "/video.v1.VideoService/GetVideo", nil, nil, nil, invoker); err == nil {
			t.Fatalf("第 %d 次调用应失败", i+1)
		}
	}
	if b.State() != StateOpen {
		t.Fatalf("连续失败 3 次后应打开，实际 %v", b.State())
	}

	// 打开后：被拒绝，且不再调用下游（calls 不再增长）。
	err := it(context.Background(), "/video.v1.VideoService/GetVideo", nil, nil, nil, invoker)
	if err == nil {
		t.Fatal("打开后应快速失败")
	}
	if *calls != 3 {
		t.Fatalf("打开后不应再触达下游，期望调用 3 次，实际 %d 次", *calls)
	}
	// 拒绝时返回带业务码的错误，网关才能翻译成 503 而不是 500。
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrServerBusy.Code {
		t.Fatalf("打开时应返回业务码 %d，实际 %v", errcode.ErrServerBusy.Code, err)
	}
}

// 冷却期结束进入半开，探测成功则闭合恢复。
func TestBreaker_HalfOpenProbeSucceeds_Closes(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b := newTestBreaker(2, clock)
	it := b.UnaryClientInterceptor()

	_, failing := alwaysInvoker(unavailable())
	for i := 0; i < 2; i++ {
		_ = it(context.Background(), "/m", nil, nil, nil, failing)
	}
	if b.State() != StateOpen {
		t.Fatalf("应已打开，实际 %v", b.State())
	}

	clock.advance(60 * time.Millisecond) // 冷却期满

	okCalls, okInvoker := alwaysInvoker(nil)
	if err := it(context.Background(), "/m", nil, nil, nil, okInvoker); err != nil {
		t.Fatalf("半开探测应放行并成功，实际 %v", err)
	}
	if *okCalls != 1 {
		t.Fatalf("探测应恰好调用一次，实际 %d", *okCalls)
	}
	if b.State() != StateClosed {
		t.Fatalf("探测成功后应闭合，实际 %v", b.State())
	}
}

// 半开探测失败则重新打开。
func TestBreaker_HalfOpenProbeFails_Reopens(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b := newTestBreaker(2, clock)
	it := b.UnaryClientInterceptor()

	_, failing := alwaysInvoker(unavailable())
	for i := 0; i < 2; i++ {
		_ = it(context.Background(), "/m", nil, nil, nil, failing)
	}
	clock.advance(60 * time.Millisecond)

	_ = it(context.Background(), "/m", nil, nil, nil, failing)
	if b.State() != StateOpen {
		t.Fatalf("半开探测失败后应重新打开，实际 %v", b.State())
	}
}

// 业务错误（带错误码封装）说明下游是活的，不计入失败。
func TestBreaker_BusinessErrorNotCounted(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b := newTestBreaker(2, clock)
	it := b.UnaryClientInterceptor()

	bizErr := grpcerr.Wrap(errcode.ErrVideoNotFound)
	_, bizInvoker := alwaysInvoker(bizErr)

	for i := 0; i < 5; i++ {
		_ = it(context.Background(), "/m", nil, nil, nil, bizInvoker)
	}
	if b.State() != StateClosed {
		t.Fatalf("业务错误不应触发熔断，实际 %v", b.State())
	}
}

// 成功调用会把闭合状态下的连续失败计数清零。
func TestBreaker_SuccessResetsFailureCount(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b := newTestBreaker(3, clock)
	it := b.UnaryClientInterceptor()

	_, failing := alwaysInvoker(unavailable())
	_, okInvoker := alwaysInvoker(nil)

	_ = it(context.Background(), "/m", nil, nil, nil, failing)
	_ = it(context.Background(), "/m", nil, nil, nil, failing)
	_ = it(context.Background(), "/m", nil, nil, nil, okInvoker) // 清零
	_ = it(context.Background(), "/m", nil, nil, nil, failing)
	_ = it(context.Background(), "/m", nil, nil, nil, failing)

	if b.State() != StateClosed {
		t.Fatalf("中途成功后失败计数应清零，实际 %v", b.State())
	}
}

// 未启用（阈值 <= 0）时永远放行。
func TestBreaker_DisabledAlwaysAllows(t *testing.T) {
	b := Disabled()
	it := b.UnaryClientInterceptor()
	calls, failing := alwaysInvoker(unavailable())

	for i := 0; i < 10; i++ {
		_ = it(context.Background(), "/m", nil, nil, nil, failing)
	}
	if b.State() != StateClosed {
		t.Fatalf("禁用时不应打开，实际 %v", b.State())
	}
	if *calls != 10 {
		t.Fatalf("禁用时应始终放行，期望 10 次，实际 %d", *calls)
	}
}

// Group 对同一 key 返回同一实例，不同 key 相互隔离。
func TestGroup_IsolatesByKey(t *testing.T) {
	g := NewGroup(Options{FailureThreshold: 3, OpenTimeout: time.Second})
	if g.Get("video") != g.Get("video") {
		t.Fatal("同一 key 应复用同一个熔断器")
	}
	if g.Get("video") == g.Get("user") {
		t.Fatal("不同 key 应各自独立")
	}
}
