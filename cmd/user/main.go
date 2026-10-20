// Command user 是用户服务的独立进程入口。
//
// 它对外提供用户域的 gRPC 契约（账号认证、个人资料与跨服务的用户简要信息查询），
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

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	"github.com/sobremesatime/chuangyue/internal/user/handler"
	userrepo "github.com/sobremesatime/chuangyue/internal/user/repository"
	"github.com/sobremesatime/chuangyue/internal/user/service"
	"github.com/sobremesatime/chuangyue/pkg/cache"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/jwt"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/mailer"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// shutdownTimeout 是收到退出信号后等待在途 RPC 完成的最长时间。
const shutdownTimeout = 10 * time.Second

func main() {
	configPath := flag.String("c", "", "配置文件路径，留空则按默认顺序查找")
	addr := flag.String("addr", ":9103", "gRPC 监听地址")
	advertise := flag.String("advertise", "127.0.0.1:9103", "注册到 Etcd 的对外地址")
	serviceName := flag.String("service", "user", "注册到 Etcd 的服务名")
	metricsAddr := flag.String("metrics-addr", ":9203", "Prometheus 指标监听地址")
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

	// 用户服务承载注册 / 登录 / 资料与跨服务展示信息：依赖 MySQL（用户库）与
	// Redis（验证码 / 发码限流），并按需构造邮件发送器与 JWT 令牌管理器。
	db, err := database.NewMySQL(cfg.MySQL)
	if err != nil {
		logger.L().Fatal("初始化 MySQL 失败", zap.Error(err))
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}

	// Redis 承载邮箱验证码、发码限流与刷新令牌重放检测，是启动硬依赖。
	rdb, err := cache.NewRedis(cfg.Redis)
	if err != nil {
		logger.L().Fatal("初始化 Redis 失败", zap.Error(err))
	}
	defer func() { _ = rdb.Close() }()

	// 邮件发送器在启动时构造：driver 配置不当（如生产环境用 log）会直接拒绝启动。
	mail, err := mailer.New(cfg.Mail, cfg.App.Env)
	if err != nil {
		logger.L().Fatal("初始化邮件发送器失败", zap.Error(err))
	}

	reg, err := registry.New(cfg.Etcd.Endpoints)
	if err != nil {
		logger.L().Fatal("初始化服务注册中心失败", zap.Error(err))
	}
	defer func() { _ = reg.Close() }()

	repo := userrepo.New(db)
	svc := service.New(
		repo,
		userrepo.NewCodeStore(rdb),
		userrepo.NewRefreshStore(rdb),
		mail,
		jwt.NewManager(cfg.JWT),
		cfg.Verify,
		cfg.Upload,
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
	userv1.RegisterUserServiceServer(server, handler.NewGRPCUserServer(svc))
	userv1.RegisterAuthServiceServer(server, handler.NewGRPCAuthServer(svc))

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

	deregister, err := reg.Register(ctx, *serviceName, *advertise)
	if err != nil {
		logger.L().Fatal("注册服务实例失败", zap.String("service", *serviceName), zap.Error(err))
	}
	defer deregister()

	logger.L().Info("用户服务启动",
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

	logger.L().Info("用户服务已退出")
}
