// Package metrics 提供 Prometheus 指标埋点。
//
// 埋四类东西：
//  1. **HTTP 入口**（网关）：请求数、耗时分布、在途请求数——按「路由模板」而非真实路径打标签，
//     避免 /videos/123 这类路径把时间序列打爆（这是 Prometheus 埋点最常见的坑）。
//  2. **gRPC 进出站**：服务端按被调方法统计，客户端按目标服务 + 方法统计（含超时 / 重试 / 熔断后的最终结果）。
//  3. **保护措施**：限流拒绝数、熔断器状态——这两个是「保护措施有没有在工作」的直接信号，
//     只看 HTTP 错误率看不到（限流返回 429、熔断返回 503，都属于「系统主动拒绝」，需要单独观察）。
//  4. **后台任务**：转码最终结果、周期任务最近成功时间与调度间隔——这两类任务不经过 HTTP / gRPC，
//     失败与停摆不会体现在前面的指标里，只能由任务自己上报。
//
// 全部指标共用进程默认注册表，因此 /metrics 里同时包含 Go 运行时与进程指标（goroutine 数、内存、CPU 等）。
//
// 关于阈值：这些指标的主要用途是**校准限流与熔断的参数**——现在配置里的数值是按经验拍的，
// 有了真实的 QPS / P99 / 错误率曲线才能定得准。
package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// namespace 是所有指标的统一前缀，避免与运行时 / 第三方指标重名。
const namespace = "chuangyue"

// Metrics 聚合本进程的指标采集器，并负责暴露 /metrics。
type Metrics struct {
	httpRequests    *prometheus.CounterVec
	httpDuration    *prometheus.HistogramVec
	httpInFlight    prometheus.Gauge
	grpcInbound     *prometheus.CounterVec
	grpcInboundDur  *prometheus.HistogramVec
	grpcOutbound    *prometheus.CounterVec
	grpcOutboundDur *prometheus.HistogramVec
	rateLimited     *prometheus.CounterVec
	transcodeJobs   *prometheus.CounterVec
	cronLastSuccess *prometheus.GaugeVec
	cronInterval    *prometheus.GaugeVec
	cronRuns        *prometheus.CounterVec
}

var (
	defaultOnce sync.Once
	defaultInst *Metrics
)

// Default 返回进程级默认实例；首次调用时完成构造与注册。
func Default() *Metrics {
	defaultOnce.Do(func() { defaultInst = newMetrics() })
	return defaultInst
}

// newMetrics 构造并注册全部采集器。
func newMetrics() *Metrics {
	m := &Metrics{
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_total",
			Help: "HTTP 请求总数（按方法 / 路由模板 / 状态码）。",
		}, []string{"method", "path", "status"}),

		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
			Help:    "HTTP 请求耗时（秒）。",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "path"}),

		httpInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "http", Name: "in_flight_requests",
			Help: "当前正在处理的 HTTP 请求数。",
		}),

		grpcInbound: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "grpc_server", Name: "requests_total",
			Help: "本服务被调用的 gRPC 请求总数（按服务 / 方法 / 状态码）。",
		}, []string{"service", "method", "code"}),

		grpcInboundDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "grpc_server", Name: "request_duration_seconds",
			Help:    "本服务处理 gRPC 请求的耗时（秒）。",
			Buckets: prometheus.DefBuckets,
		}, []string{"service", "method"}),

		grpcOutbound: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "grpc_client", Name: "requests_total",
			Help: "出站 gRPC 调用总数（按目标服务 / 方法 / 状态码，含重试与熔断后的最终结果）。",
		}, []string{"target", "method", "code"}),

		grpcOutboundDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "grpc_client", Name: "request_duration_seconds",
			Help:    "出站 gRPC 调用的总耗时（秒，含重试等待）。",
			Buckets: prometheus.DefBuckets,
		}, []string{"target", "method"}),

		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "ratelimit", Name: "rejected_total",
			Help: "被限流拒绝的请求数（按维度：ip / user）。",
		}, []string{"dimension"}),

		transcodeJobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "transcode", Name: "jobs_total",
			Help: "转码任务最终结果数（result：success / failed）。只统计已成功回写视频服务的结果。",
		}, []string{"result"}),

		// 定时任务的「停摆」无法靠错误计数发现（任务可能只是不再被调度），
		// 因此用「最近一次成功完成的时间戳 + 调度间隔」两个 gauge 配合告警：
		// 超过两个周期没有更新即视为停摆，见 deploy/monitoring/prometheus/alerts.yml。
		cronLastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "cron", Name: "job_last_success_timestamp_seconds",
			Help: "定时任务最近一次成功完成的时间戳（Unix 秒）。",
		}, []string{"job"}),

		cronInterval: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "cron", Name: "job_interval_seconds",
			Help: "定时任务的调度间隔（秒，按 cron 表达式推算）。",
		}, []string{"job"}),

		cronRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "cron", Name: "job_runs_total",
			Help: "定时任务执行次数（result：success / failed / skipped）。",
		}, []string{"job", "result"}),
	}

	for _, c := range []prometheus.Collector{
		m.httpRequests, m.httpDuration, m.httpInFlight,
		m.grpcInbound, m.grpcInboundDur,
		m.grpcOutbound, m.grpcOutboundDur,
		m.rateLimited,
		m.transcodeJobs,
		m.cronLastSuccess, m.cronInterval, m.cronRuns,
	} {
		register(c)
	}
	return m
}

// register 注册采集器；已注册（如测试里重复构造）时忽略，其余错误只记警告。
func register(c prometheus.Collector) {
	if err := prometheus.DefaultRegisterer.Register(c); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			return
		}
		logger.L().Warn("注册 Prometheus 指标失败", zap.Error(err))
	}
}

// RegisterBreakerState 暴露某个下游服务的熔断器状态。
//
// 取值 0=闭合 1=半开 2=打开。开着的熔断器意味着某个下游正在持续失败——这是最需要告警的信号之一。
func (m *Metrics) RegisterBreakerState(service string, state func() float64) {
	register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   namespace,
		Subsystem:   "breaker",
		Name:        "state",
		Help:        "熔断器状态：0=闭合 1=半开 2=打开。",
		ConstLabels: prometheus.Labels{"service": service},
	}, state))
}

// IncRateLimited 记录一次限流拒绝，dimension 取 "ip" / "user"。
func (m *Metrics) IncRateLimited(dimension string) {
	m.rateLimited.WithLabelValues(dimension).Inc()
}

// ObserveTranscodeResult 记录一次转码任务的最终结果，result 取 "success" / "failed"。
//
// 只应在结果已成功回写视频服务之后调用：回写失败的任务会重投，
// 提前计数会把同一次转码算成多次。
func (m *Metrics) ObserveTranscodeResult(result string) {
	m.transcodeJobs.WithLabelValues(result).Inc()
}

// RegisterCronJob 登记一个周期任务，写入调度间隔并初始化「最近成功时间」。
//
// 初始化为当前时间而不是 0：进程刚启动时任务还没到第一个执行点，
// 若从 0 开始会让长周期任务（如每小时一次）在启动后立刻命中停摆告警。
func (m *Metrics) RegisterCronJob(job string, interval time.Duration) {
	m.cronInterval.WithLabelValues(job).Set(interval.Seconds())
	m.cronLastSuccess.WithLabelValues(job).Set(float64(time.Now().Unix()))
}

// ObserveCronRun 记录一次周期任务执行结果，result 取 "success" / "failed" / "skipped"。
//
// success 的语义是「任务函数执行完成且未 panic」，会刷新 last_success；
// 任务内部的业务失败（如清理某条记录失败）由任务自己打日志 / 业务指标体现，
// 本包不替它判断——因此停摆告警的判据是「最近一次成功时间」而不是「有没有报错」。
func (m *Metrics) ObserveCronRun(job, result string) {
	m.cronRuns.WithLabelValues(job, result).Inc()
	if result == "success" {
		m.cronLastSuccess.WithLabelValues(job).Set(float64(time.Now().Unix()))
	}
}

// Handler 返回 /metrics 的 HTTP 处理器（同时包含 Go 运行时与进程指标）。
func (m *Metrics) Handler() http.Handler { return promhttp.Handler() }

// GinMiddleware 统计 HTTP 请求数、耗时与在途数。
//
// 标签用 c.FullPath()（路由模板，如 /api/v1/videos/:id）而不是真实路径：
// 后者会把每个视频 ID 都变成一条时间序列，很快就把 Prometheus 打爆。
// 未命中路由（404）统一记为 unmatched。
func (m *Metrics) GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}
		method := c.Request.Method

		m.httpInFlight.Inc()
		start := time.Now()
		c.Next()
		m.httpInFlight.Dec()

		m.httpRequests.WithLabelValues(method, path, strconv.Itoa(c.Writer.Status())).Inc()
		m.httpDuration.WithLabelValues(method, path).Observe(time.Since(start).Seconds())
	}
}

// UnaryServerInterceptor 统计本服务处理一元请求的数量与耗时。
func (m *Metrics) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		service, method := splitFullMethod(info.FullMethod)

		start := time.Now()
		resp, err := handler(ctx, req)

		m.grpcInbound.WithLabelValues(service, method, status.Code(err).String()).Inc()
		m.grpcInboundDur.WithLabelValues(service, method).Observe(time.Since(start).Seconds())
		return resp, err
	}
}

// StreamServerInterceptor 统计本服务处理流式请求的数量与耗时（以整条流的时长为耗时）。
func (m *Metrics) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		service, method := splitFullMethod(info.FullMethod)

		start := time.Now()
		err := handler(srv, ss)

		m.grpcInbound.WithLabelValues(service, method, status.Code(err).String()).Inc()
		m.grpcInboundDur.WithLabelValues(service, method).Observe(time.Since(start).Seconds())
		return err
	}
}

// UnaryClientInterceptor 统计到某个下游的一元调用。
//
// target 是链路里配置的服务名（如 video）；method 是被调方法名。
// 它挂在拦截器链的**最外层**，因此统计到的是「一次逻辑调用」的最终结果与总耗时——
// 包含重试等待与熔断开路时的快速失败，这正是调用方视角的真实体验。
func (m *Metrics) UnaryClientInterceptor(target string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		_, name := splitFullMethod(method)

		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)

		m.grpcOutbound.WithLabelValues(target, name, status.Code(err).String()).Inc()
		m.grpcOutboundDur.WithLabelValues(target, name).Observe(time.Since(start).Seconds())
		return err
	}
}

// StartServer 在 addr 上启动 /metrics 服务，并顺带暴露 /debug/pprof，在 ctx 取消时优雅关闭。
//
// 返回错误表示**监听失败**（如端口被占用）——由调用方决定是致命还是仅告警，
// 指标服务不该成为主服务的启动前提。
//
// 安全提示：该端口同时提供 /metrics 与 /debug/pprof，**生产环境不得对公网开放**，
// 应在接入层（Nginx / Ingress）限制为监控网段可达。pprof 的 /profile 与 /trace
// 会消耗可观的 CPU，暴露即等于给出一个可被利用的放大点。
func (m *Metrics) StartServer(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	registerPprof(mux)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Error("metrics 服务异常退出", zap.String("addr", addr), zap.Error(err))
		}
	}()

	logger.L().Info("metrics 服务已启动", zap.String("addr", addr), zap.String("path", "/metrics"))
	return nil
}

// registerPprof 把 pprof 端点登记到 mux 上。
//
// net/http/pprof 默认只把处理器挂到 http.DefaultServeMux，而本进程用的是自建 mux，
// 不会自动继承——必须显式登记，否则 /debug/pprof 全部 404。
// 各类命名 profile（heap / goroutine / allocs / block / mutex …）由 Index 按路径分发，
// 因此这里只需登记 Index 与四个特殊端点（cmdline / profile / symbol / trace）。
func registerPprof(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// PprofHandler 返回 pprof 的处理器，供网关在自己的 gin 引擎上复用。
//
// 网关的 /metrics 挂在 gin 路由上、不经过 StartServer，因此 pprof 无法借道后者；
// 这里集中登记一次，网关把它整体代理到 /debug/pprof 子树即可。
func PprofHandler() http.Handler {
	mux := http.NewServeMux()
	registerPprof(mux)
	return mux
}

// splitFullMethod 把 "/video.v1.VideoService/GetVideo" 拆成服务名与方法名。
func splitFullMethod(full string) (service, method string) {
	full = strings.TrimPrefix(full, "/")
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[:i], full[i+1:]
	}
	return full, ""
}
