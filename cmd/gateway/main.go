// Command gateway 是窗月平台的 API 网关入口（当前版本同时承担单体后端职责）。
//
// 启动流程：加载配置 -> 初始化日志 -> 初始化基础设施 -> 装配路由 -> 启动 HTTP 服务，
// 并支持优雅退出（收到 SIGINT / SIGTERM 后等待在途请求处理完毕）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	feedv1 "github.com/sobremesatime/chuangyue/api/gen/feed/v1"
	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	searchv1 "github.com/sobremesatime/chuangyue/api/gen/search/v1"
	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/gateway/router"
	"github.com/sobremesatime/chuangyue/pkg/cache"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/grpcclient"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/ratelimit"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// shutdownTimeout 优雅退出时等待在途请求的最长时间。
const shutdownTimeout = 10 * time.Second

func main() {
	configPath := flag.String("c", "", "配置文件路径，留空则按默认顺序查找")
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

	// 进程级信号上下文：既让追踪在退出时 flush，也用于 HTTP 服务的优雅关闭。
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 链路追踪：未配置 endpoint 时自动禁用（no-op），失败只告警——追踪不该阻断网关启动。
	if err := tracing.Setup(ctx, "gateway", cfg.Tracing.Endpoint, cfg.Tracing.SampleRatio); err != nil {
		logger.L().Warn("初始化链路追踪失败，追踪不可用", zap.Error(err))
	}

	// 出站调用的熔断器：进程级一次性配置，之后 Dial 出来的连接共用同一套熔断状态。
	grpcclient.ConfigureCircuitBreaker(cfg.CircuitBreaker)

	// 服务注册中心：用户域、视频域与互动域都已全量切到服务间调用，
	// 网关需连 Etcd 按服务名发现各服务实例。
	reg, err := registry.New(cfg.Etcd.Endpoints)
	if err != nil {
		logger.L().Fatal("初始化服务注册中心失败", zap.Error(err))
	}
	defer func() { _ = reg.Close() }()
	registry.RegisterResolver(reg)

	userConn, err := grpcclient.Dial("user")
	if err != nil {
		logger.L().Fatal("建立用户服务连接失败", zap.Error(err))
	}
	defer func() { _ = userConn.Close() }()

	videoConn, err := grpcclient.Dial("video")
	if err != nil {
		logger.L().Fatal("建立视频服务连接失败", zap.Error(err))
	}
	defer func() { _ = videoConn.Close() }()

	// 互动域已全量切到服务间调用：网关不再持有互动库，全部互动接口经此连接转发。
	interactionConn, err := grpcclient.Dial("interaction")
	if err != nil {
		logger.L().Fatal("建立互动服务连接失败", zap.Error(err))
	}
	defer func() { _ = interactionConn.Close() }()

	// 推荐域：首页推荐流由独立的推荐服务计算，网关经此连接读取。
	feedConn, err := grpcclient.Dial("feed")
	if err != nil {
		logger.L().Fatal("建立推荐服务连接失败", zap.Error(err))
	}
	defer func() { _ = feedConn.Close() }()

	// 搜索域：视频检索由独立的搜索服务承担，网关经此连接读取。
	searchConn, err := grpcclient.Dial("search")
	if err != nil {
		logger.L().Fatal("建立搜索服务连接失败", zap.Error(err))
	}
	defer func() { _ = searchConn.Close() }()

	// 限流需要 Redis。Redis 不可用**不阻断启动**：只把限流停用（fail-open），
	// 与「保护组件自己故障不该把站点一起带走」的原则一致。
	var ipLimiter, userLimiter *ratelimit.Limiter
	if cfg.RateLimit.Enabled {
		rdb, err := cache.NewRedis(cfg.Redis)
		if err != nil {
			logger.L().Warn("Redis 不可用，网关限流已停用", zap.Error(err))
		} else {
			defer func() { _ = rdb.Close() }()
			ipLimiter = ratelimit.New(rdb, "rl:ip:", cfg.RateLimit.IPBurst, cfg.RateLimit.IPPerSecond)
			userLimiter = ratelimit.New(rdb, "rl:user:", cfg.RateLimit.UserBurst, cfg.RateLimit.UserPerSecond)
			logger.L().Info("网关限流已启用",
				zap.Float64("ip_per_second", cfg.RateLimit.IPPerSecond),
				zap.Int("ip_burst", cfg.RateLimit.IPBurst),
				zap.Float64("user_per_second", cfg.RateLimit.UserPerSecond),
				zap.Int("user_burst", cfg.RateLimit.UserBurst))
		}
	}

	engine := router.New(
		cfg,
		router.Limiters{IP: ipLimiter, User: userLimiter},
		userv1.NewAuthServiceClient(userConn),
		userv1.NewUserServiceClient(userConn),
		videov1.NewVideoServiceClient(videoConn),
		videov1.NewUploadServiceClient(videoConn),
		videov1.NewReportServiceClient(videoConn),
		interactionv1.NewUserInteractionServiceClient(interactionConn),
		interactionv1.NewCommentServiceClient(interactionConn),
		interactionv1.NewDanmakuServiceClient(interactionConn),
		interactionv1.NewNotificationServiceClient(interactionConn),
		feedv1.NewFeedServiceClient(feedConn),
		searchv1.NewSearchServiceClient(searchConn),
	)

	srv := &http.Server{
		Addr:    cfg.App.Addr,
		Handler: engine,
	}

	go func() {
		logger.L().Info("网关启动",
			zap.String("app", cfg.App.Name),
			zap.String("env", cfg.App.Env),
			zap.String("addr", cfg.App.Addr),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Fatal("HTTP 服务异常退出", zap.Error(err))
		}
	}()

	waitForShutdown(ctx, srv)
}

// waitForShutdown 阻塞等待退出信号，并在收到信号后优雅关闭 HTTP 服务。
func waitForShutdown(ctx context.Context, srv *http.Server) {
	<-ctx.Done()

	logger.L().Info("收到退出信号，开始优雅关闭...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.L().Error("关闭 HTTP 服务出错", zap.Error(err))
	}
	logger.L().Info("网关已退出")
}
