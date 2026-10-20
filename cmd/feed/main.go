// Command feed 是首页推荐服务的独立进程入口。
//
// feed 是聚合者，不持有任何业务表：它把 video 的候选池与 interaction 的用户信号
// 拉回来做个性化重排，因此这里不连 MySQL，依赖只有 Redis（结果缓存）与 Etcd（注册 / 发现）。
// 启动后把自身地址注册到 Etcd，供网关及其它调用方按服务名发现。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	feedv1 "github.com/sobremesatime/chuangyue/api/gen/feed/v1"
	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/feed/handler"
	"github.com/sobremesatime/chuangyue/internal/feed/service"
	"github.com/sobremesatime/chuangyue/pkg/cache"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/cron"
	"github.com/sobremesatime/chuangyue/pkg/grpcclient"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// shutdownTimeout 是收到退出信号后等待在途 RPC 完成的最长时间。
const shutdownTimeout = 10 * time.Second

// rankRefreshLockTTL 是首页热度榜预热任务的单飞锁租期。
//
// 与 pkg/lock 的取值规则一致：需大于「单轮预热的最长耗时」（两次跨服务 RPC + 一次写缓存，
// 正常在秒级），同时小于执行间隔（默认 1min）。30s 对默认间隔留有足够余量，
// 崩溃后最多跨过一轮即自愈。
const rankRefreshLockTTL = 30 * time.Second

func main() {
	configPath := flag.String("c", "", "配置文件路径，留空则按默认顺序查找")
	addr := flag.String("addr", ":9104", "gRPC 监听地址")
	advertise := flag.String("advertise", "127.0.0.1:9104", "注册到 Etcd 的对外地址")
	serviceName := flag.String("service", "feed", "注册到 Etcd 的服务名")
	metricsAddr := flag.String("metrics-addr", ":9204", "Prometheus 指标监听地址")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	if err := logger.Init(cfg.Log.Level, cfg.Log.Format, cfg.Log.Output); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// 出站调用统一启用熔断：下游持续失败时快速失败，不再白等超时。
	grpcclient.ConfigureCircuitBreaker(cfg.CircuitBreaker)

	// feed 不持有表，因此不连 MySQL；只需 Redis 做结果缓存。
	rdb, err := cache.NewRedis(cfg.Redis)
	if err != nil {
		logger.L().Fatal("初始化 Redis 失败", zap.Error(err))
	}
	defer func() { _ = rdb.Close() }()

	reg, err := registry.New(cfg.Etcd.Endpoints)
	if err != nil {
		logger.L().Fatal("初始化服务注册中心失败", zap.Error(err))
	}
	defer func() { _ = reg.Close() }()

	// 注册解析器后，客户端才能用 etcd:///video、etcd:///interaction 按服务名拨号。
	registry.RegisterResolver(reg)

	videoConn, err := grpcclient.Dial("video")
	if err != nil {
		logger.L().Fatal("建立视频服务连接失败", zap.Error(err))
	}
	defer func() { _ = videoConn.Close() }()

	interactionConn, err := grpcclient.Dial("interaction")
	if err != nil {
		logger.L().Fatal("建立互动服务连接失败", zap.Error(err))
	}
	defer func() { _ = interactionConn.Close() }()

	svc := service.New(
		videov1.NewVideoServiceClient(videoConn),
		interactionv1.NewInteractionServiceClient(interactionConn),
		rdb,
	)

	// 进程级信号上下文：既让追踪在退出时 flush，也用于实例续租与优雅退出。
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 链路追踪：endpoint 未配置时自动禁用（no-op）；失败只告警，不阻断服务启动。
	if err := tracing.Setup(ctx, *serviceName, cfg.Tracing.Endpoint, cfg.Tracing.SampleRatio); err != nil {
		logger.L().Warn("初始化链路追踪失败，追踪不可用", zap.Error(err))
	}

	// 服务端拦截器：把上游经 metadata 传来的 trace_id 与调用者身份取回 context，
	// 并统计本服务被调用的指标（方法 / 状态码 / 耗时）；
	// otelgrpc 的 StatsHandler 为每个入站 RPC 生成 server span 并接住上游的 traceparent。
	server := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(metrics.Default().UnaryServerInterceptor(), tracing.UnaryServerInterceptor(), identity.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(metrics.Default().StreamServerInterceptor(), tracing.StreamServerInterceptor(), identity.StreamServerInterceptor()),
	)
	feedv1.RegisterFeedServiceServer(server, handler.NewGRPCFeedServer(svc))

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.L().Fatal("监听 gRPC 端口失败", zap.String("addr", *addr), zap.Error(err))
	}

	// 注册实例沿用上面的进程 ctx：信号到达后停止续租，配合显式注销尽快下线。

	// Prometheus 指标服务：与主服务同生命周期（ctx 取消即关闭）。
	// 监听失败只告警不致命——指标不该成为主服务能不能启动的前提。
	if err := metrics.Default().StartServer(ctx, *metricsAddr); err != nil {
		logger.L().Warn("启动 metrics 服务失败，指标不可抓取", zap.String("addr", *metricsAddr), zap.Error(err))
	}

	// 周期任务（配置项 jobs.enabled 可整体关闭）：定时预热首页热度榜。
	//
	// 未登录首页的候选池来自视频服务的实时排序，缓存过期后的第一个请求要跨服务回源——
	// 既是延迟尖峰，也是缓存击穿窗口。按缓存 TTL 的节奏提前算好写回，读请求恒命中缓存。
	// 单飞锁保证多副本下同一轮只有一个实例真正回源；feed 本身就强依赖 Redis，锁总是可用。
	var sched *cron.Scheduler
	if cfg.Jobs.Enabled {
		sched = cron.New(rdb)
		if err := sched.Add("feed-rank-refresh", cfg.Jobs.RankRefresh, svc.RefreshRanking,
			cron.WithLock(rankRefreshLockTTL)); err != nil {
			logger.L().Warn("注册首页热度榜预热任务失败", zap.Error(err))
		}
		sched.Start(ctx)
	}

	deregister, err := reg.Register(ctx, *serviceName, *advertise)
	if err != nil {
		logger.L().Fatal("注册服务实例失败", zap.String("service", *serviceName), zap.Error(err))
	}
	defer deregister()

	logger.L().Info("首页推荐服务启动",
		zap.String("service", *serviceName),
		zap.String("addr", lis.Addr().String()),
		zap.String("advertise", *advertise),
	)

	go func() {
		if err := server.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.L().Fatal("gRPC 服务异常退出", zap.Error(err))
		}
	}()

	<-ctx.Done()
	logger.L().Info("收到退出信号，开始优雅关闭...")

	// 先停调度：不再起新的周期任务，并等在途的那一轮收尾（见 pkg/cron 的 Stop）。
	if sched != nil {
		sched.Stop()
	}

	// GracefulStop 会阻塞到在途 RPC 结束，加超时兜底避免个别卡死请求拖住进程。
	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(shutdownTimeout):
		server.Stop()
	}

	logger.L().Info("首页推荐服务已退出")
}
