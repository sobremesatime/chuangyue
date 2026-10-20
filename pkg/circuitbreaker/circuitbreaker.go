// Package circuitbreaker 提供跨服务调用的熔断能力（经典三态：闭合 / 打开 / 半开）。
//
// 解决什么问题：下游持续不可用时，调用方每个请求都要白等一次超时（本项目统一 5s）——
// 用户看到的是全站卡顿。熔断器在连续失败达到阈值后**打开**，直接快速失败，
// 等冷却期过去再**半开**放少量探测请求；探测成功即**闭合**恢复正常。
//
// 什么时候用：它只在「下游多实例、局部慢 / 局部挂」时收益明显。若下游是单实例、
// 要么整体好要么整体挂，熔断主要起「别再白等超时」的止损作用，配合降级使用。
//
// 与重试的分工：重试处理「一次性抖动」（重试往往就好了），熔断处理「持续故障」
// （重试也没用，别再试了）。因此二者在两个层次上：熔断在重试**之外**，
// 观察到的是「一次逻辑调用（含重试）的最终结果」，而不是每次尝试。
package circuitbreaker

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
)

// State 是熔断器状态。
type State int

const (
	// StateClosed 闭合：正常放行，累计失败。
	StateClosed State = iota
	// StateOpen 打开：一律快速失败，直到冷却期结束。
	StateOpen
	// StateHalfOpen 半开：放少量探测请求，据结果决定恢复还是再次打开。
	StateHalfOpen
)

// String 便于日志与测试断言。
func (s State) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// Options 是单个熔断器的参数。
type Options struct {
	// FailureThreshold 是连续失败多少次后打开。<= 0 表示禁用（永远放行）。
	FailureThreshold int
	// OpenTimeout 是打开后等待多久进入半开。
	OpenTimeout time.Duration
	// HalfOpenMax 是半开状态下允许的并发探测请求数。
	HalfOpenMax int
	// Now 用于注入时钟，便于测试；为 nil 时使用 time.Now。
	Now func() time.Time
}

// Breaker 是单个下游目标的熔断器，可安全并发使用。
type Breaker struct {
	mu     sync.Mutex
	opts   Options
	state  State
	failed int // 闭合状态下的连续失败次数
	opened time.Time
	probes int // 半开状态下在途的探测请求数
}

// NewBreaker 构造熔断器。FailureThreshold <= 0 时返回始终放行的禁用实例。
func NewBreaker(opts Options) *Breaker {
	if opts.OpenTimeout <= 0 {
		opts.OpenTimeout = 10 * time.Second
	}
	if opts.HalfOpenMax <= 0 {
		opts.HalfOpenMax = 1
	}
	return &Breaker{opts: opts}
}

// Disabled 返回一个永远放行的熔断器，供「未启用熔断」时占位，避免调用方到处判空。
func Disabled() *Breaker { return NewBreaker(Options{}) }

// now 返回当前时间（测试可注入）。
func (b *Breaker) now() time.Time {
	if b.opts.Now != nil {
		return b.opts.Now()
	}
	return time.Now()
}

// State 返回当前状态（供测试与观测使用）。
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Allow 判断是否放行本次调用。
//
// 放行时返回一个 done 回调，调用方必须用它回报结果：done(true) 成功、done(false) 失败。
// 被拒绝时 ok 为 false，此时不应调用下游。
func (b *Breaker) Allow() (done func(success bool), ok bool) {
	if b.opts.FailureThreshold <= 0 {
		// 禁用：直接放行，不做任何记账。
		return func(bool) {}, true
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateOpen:
		// 冷却期未到：继续快速失败。
		if b.now().Sub(b.opened) < b.opts.OpenTimeout {
			return nil, false
		}
		// 冷却期满：转入半开，用少量探测请求试探下游是否恢复。
		b.state = StateHalfOpen
		b.probes = 0
	case StateHalfOpen:
		// 半开期间只放行限定并发，避免流量瞬间打回故障的下游。
		if b.probes >= b.opts.HalfOpenMax {
			return nil, false
		}
	}

	if b.state == StateHalfOpen {
		b.probes++
	}
	return b.report, true
}

// report 汇总一次调用的结果，按当前状态推进状态机。
func (b *Breaker) report(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == StateHalfOpen {
		if b.probes > 0 {
			b.probes--
		}
		if success {
			// 探测成功：下游已恢复，闭合并把失败计数清零。
			b.state = StateClosed
			b.failed = 0
			return
		}
		b.trip()
		return
	}

	if success {
		b.failed = 0
		return
	}
	b.failed++
	if b.failed >= b.opts.FailureThreshold {
		b.trip()
	}
}

// trip 打开熔断器。
func (b *Breaker) trip() {
	b.state = StateOpen
	b.opened = b.now()
	b.failed = 0
	b.probes = 0
}

// UnaryClientInterceptor 返回绑定到「某个下游服务」的一元调用拦截器。
//
// 被拒绝时返回的错误带业务错误码封装（ErrServerBusy）：这样网关能把它翻译成
// HTTP 503「服务繁忙」，而不是笼统的 500；同时因为它带错误码封装，
// 其它熔断器也不会把它误记为可用性故障。
func (b *Breaker) UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		done, ok := b.Allow()
		if !ok {
			return grpcerr.Wrap(errcode.ErrServerBusy.WithMsg("下游服务熔断中，请稍后再试"))
		}

		err := invoker(ctx, method, req, reply, cc, opts...)
		done(!isAvailabilityFailure(err))
		return err
	}
}

// isAvailabilityFailure 判断错误是否属于「下游不可用」类故障。
//
// 只有这类错误才累计到熔断计数：
//   - 带业务错误码封装的错误（如 404、违规参数、转码队列繁忙）说明下游是**活的**，
//     只是这次请求业务上不通过，记为成功（好让上一次的失败计数清零）；
//   - 纯传输层错误（不可用 / 超时 / 限流耗尽）才是真正的可用性故障。
func isAvailabilityFailure(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := grpcerr.Unwrap(err); ok {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

// Group 按服务名维护一组熔断器，供进程内共享（同一服务的多条连接共用同一状态）。
type Group struct {
	mu       sync.Mutex
	opts     Options
	breakers map[string]*Breaker
}

// NewGroup 构造熔断器组。
func NewGroup(opts Options) *Group {
	return &Group{opts: opts, breakers: make(map[string]*Breaker)}
}

// Get 返回某个服务名对应的熔断器，不存在时按组参数创建。
func (g *Group) Get(key string) *Breaker {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b, ok := g.breakers[key]; ok {
		return b
	}
	b := NewBreaker(g.opts)
	g.breakers[key] = b
	return b
}
