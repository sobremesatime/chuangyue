// Package grpcclient 统一内部服务间的 gRPC 拨号方式。
//
// 服务之间不经 IP、只认服务名：地址由 Etcd 注册与发现提供，
// 因此这里把「服务名 -> 连接」的约定（etcd:/// 前缀、内网明文凭证）收口，
// 避免各 cmd 入口各自拼 scheme 与传输凭证选项，出现口径不一致。
package grpcclient

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"

	"github.com/sobremesatime/chuangyue/pkg/circuitbreaker"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/grpcretry"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// 出站调用的熔断器配置与实例：都在进程级共享。
//
// 为什么放包级而不是做成 Dial 的参数：熔断状态必须**跨连接共享**才有意义
// （同一服务的多条连接应共用一个状态），而 Dial 每次调用都会新建一个连接。
// 未调用 ConfigureCircuitBreaker 时按「禁用」处理——不熔断，行为与改造前一致。
var (
	breakerMu sync.Mutex
	breakers  *circuitbreaker.Group
)

// ConfigureCircuitBreaker 进程级一次性配置出站调用的熔断器。
//
// Enabled 为 false 时按禁用处理（等价于阈值 <= 0：永远放行、不记账）。
// 各进程在启动时调用一次即可，之后所有 Dial 出来的连接共用这套熔断状态。
func ConfigureCircuitBreaker(cfg config.BreakerConfig) {
	if !cfg.Enabled {
		cfg.FailureThreshold = 0
	}
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breakers = circuitbreaker.NewGroup(circuitbreaker.Options{
		FailureThreshold: cfg.FailureThreshold,
		OpenTimeout:      cfg.OpenTimeout,
		HalfOpenMax:      cfg.HalfOpenMax,
	})
}

// breakerFor 返回某个服务名对应的熔断器；未配置时返回禁用实例（永远放行）。
//
// 顺便把该熔断器的状态注册成指标（可在 /metrics 看到，值为 0=闭合 1=半开 2=打开），
// 便于对「某个下游正在被熔断」告警。注册是幂等的，重复调用会自动忽略。
func breakerFor(service string) *circuitbreaker.Breaker {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	if breakers == nil {
		return circuitbreaker.Disabled()
	}
	b := breakers.Get(service)
	metrics.Default().RegisterBreakerState(service, func() float64 { return float64(b.State()) })
	return b
}

// defaultUnaryTimeout 是单次一元（unary）跨服务调用的默认超时。
//
// 跨服务调用的耗时不可控，不给上限就等于把下游的故障直接变成调用方的无限等待。
// 之所以只约束一元调用、不给流式调用设统一上限：上传 / 头像这类长连接的耗时取决于
// 用户与文件大小，统一截断会误杀正常的大文件上传；它们由网关的 HTTP 请求 context 兜底。
const defaultUnaryTimeout = 5 * time.Second

// Dial 按 Etcd 服务名建立到目标服务的 gRPC 连接。
//
// 调用前需先经 registry.RegisterResolver 注册解析器，否则 etcd:/// 无法解析。
// 返回的连接是惰性建立的：建连成功不代表对端已就绪，首次调用时才真正拨号。
//
// 所有出站调用统一挂客户端拦截器（由外到内）：
//   - metrics：按「目标服务 + 方法 + 状态码」统计调用数与总耗时（含重试与熔断的影响）；
//   - tracing：把请求 context 里的 trace_id 带进 metadata，下游据此串联日志（含流式调用）；
//   - identity：把已认证身份带进 metadata，下游据此取调用者，而不用在请求消息里带 user_id；
//   - 熔断：下游持续失败到阈值就快速失败，不再白等超时（见 pkg/circuitbreaker）；
//   - 超时：给没有 deadline 的一元调用补上默认超时；
//   - 重试：只对白名单内的幂等读方法重试瞬时故障（见 pkg/grpcretry）。
//
// 此外挂上 otelgrpc 的客户端 StatsHandler：它为每次出站调用生成一个 client span，
// 并把 context 里的 span 经 `traceparent` metadata 传给下游，从而在 Jaeger 里连成跨服务调用树。
// 这与上面的 tracing 拦截器（x-trace-id，服务日志用）互不冲突、并行工作。
//
// 三个关注点（熔断 / 超时 / 重试）的嵌套顺序是有意为之：
//   - 超时在重试之外 → 「首次 + 若干次重试」共享同一条 deadline，总耗时仍有上限；
//   - 熔断在超时与重试之外 → 它统计的是「一次逻辑调用（含重试）的最终结果」，
//     而不是每次尝试；打开时直接短路，连超时都不用等；
//   - 指标在最外层 → 记录的是调用方视角的真实耗时（重试等待与熔断快速失败都算在内）。
func Dial(service string) (*grpc.ClientConn, error) {
	return grpc.NewClient("etcd:///"+service,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithChainUnaryInterceptor(
			metrics.Default().UnaryClientInterceptor(service),
			tracing.UnaryClientInterceptor(),
			identity.UnaryClientInterceptor(),
			breakerFor(service).UnaryClientInterceptor(),
			unaryTimeoutInterceptor(defaultUnaryTimeout),
			grpcretry.UnaryClientInterceptor(),
		),
		grpc.WithChainStreamInterceptor(
			tracing.StreamClientInterceptor(),
			identity.StreamClientInterceptor(),
		),
	)
}

// unaryTimeoutInterceptor 给未设置 deadline 的一元调用补一个超时。
//
// 已有更早的 deadline 时沿用调用方的设定，不覆盖：上层若因业务需要收紧（或不设）超时，
// 这里不该擅自改口径。
func unaryTimeoutInterceptor(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
