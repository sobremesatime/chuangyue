// Command search 是视频检索服务的独立进程入口。
//
// 检索不持有任何业务表：启用 ES 时查询自己的检索索引（索引由视频变更事件异步追平），
// 命中后经视频服务补全卡片；未启用或索引不可用时退回视频服务的库内检索。
// 因此这里不连 MySQL，依赖是 Etcd（注册 / 发现）、视频服务，以及可选的 ES / Kafka。
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

	searchv1 "github.com/sobremesatime/chuangyue/api/gen/search/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/search/handler"
	"github.com/sobremesatime/chuangyue/internal/search/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/es"
	"github.com/sobremesatime/chuangyue/pkg/grpcclient"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/mq"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// shutdownTimeout 是收到退出信号后等待在途 RPC 完成的最长时间。
const shutdownTimeout = 10 * time.Second

// indexRetryInterval 是索引初始化 / 消费失败后的重连间隔。
const indexRetryInterval = 5 * time.Second

func main() {
	configPath := flag.String("c", "", "配置文件路径，留空则按默认顺序查找")
	addr := flag.String("addr", ":9105", "gRPC 监听地址")
	advertise := flag.String("advertise", "127.0.0.1:9105", "注册到 Etcd 的对外地址")
	serviceName := flag.String("service", "search", "注册到 Etcd 的服务名")
	metricsAddr := flag.String("metrics-addr", ":9205", "Prometheus 指标监听地址")
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

	reg, err := registry.New(cfg.Etcd.Endpoints)
	if err != nil {
		logger.L().Fatal("初始化服务注册中心失败", zap.Error(err))
	}
	defer func() { _ = reg.Close() }()

	// 注册解析器后，客户端才能用 etcd:///video 按服务名拨号。
	registry.RegisterResolver(reg)

	// 检索不持有业务表：命中的视频经视频服务的检索接口读取。
	videoConn, err := grpcclient.Dial("video")
	if err != nil {
		logger.L().Fatal("建立视频服务连接失败", zap.Error(err))
	}
	defer func() { _ = videoConn.Close() }()
	videoClient := videov1.NewVideoServiceClient(videoConn)

	// 检索索引是可选后端：启用时查询 ES，不启用 / 初始化失败时退回库内检索。
	// 初始化失败只告警、不阻断启动——搜索服务本身仍能通过视频服务工作。
	var (
		indexClient *es.Client
		svcOpts     []service.Option
	)
	if cfg.Search.ES.Enabled {
		indexClient, err = es.New(es.Config{
			Addresses: cfg.Search.ES.Addresses,
			Username:  cfg.Search.ES.Username,
			Password:  cfg.Search.ES.Password,
			Index:     cfg.Search.ES.Index,
			Timeout:   cfg.Search.ES.Timeout,
		})
		if err != nil {
			logger.L().Warn("初始化检索索引客户端失败，检索退回库内查询", zap.Error(err))
		} else {
			svcOpts = append(svcOpts, service.WithSearcher(indexClient))
		}
	}

	svc := service.New(videoClient, svcOpts...)

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
	searchv1.RegisterSearchServiceServer(server, handler.NewGRPCSearchServer(svc))

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

	logger.L().Info("搜索服务启动",
		zap.String("service", *serviceName),
		zap.String("addr", lis.Addr().String()),
		zap.String("advertise", *advertise),
	)

	go func() {
		if err := server.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.L().Fatal("gRPC 服务异常退出", zap.Error(err))
		}
	}()

	// 检索索引的同步链路（仅在启用 ES 时）：
	//   1. 确保 Kafka topic 与 ES 索引存在；
	//   2. 索引为空时从视频服务全量回填（首次部署 / 索引重建后没有历史事件可消费）；
	//   3. 持续消费视频变更事件，增量更新索引。
	//
	// 任一步失败都退避重试，且不阻断搜索服务启动：索引不可用时检索会自动退回库内查询。
	if indexClient != nil {
		indexSvc := service.NewIndexService(indexClient)
		indexConsumer := mq.NewConsumer(cfg.Kafka.Brokers, cfg.Kafka.IndexTopic, cfg.Kafka.IndexGroup, indexSvc.HandleVideoIndex)
		defer func() { _ = indexConsumer.Close() }()

		go func() {
			for {
				if err := mq.EnsureTopic(cfg.Kafka.Brokers, cfg.Kafka.IndexTopic, 3); err != nil {
					logger.L().Warn("确保视频索引 topic 失败，稍后重试", zap.Error(err))
				} else if err := indexClient.EnsureIndex(ctx); err != nil {
					logger.L().Warn("确保检索索引失败，稍后重试", zap.Error(err))
				} else if err := service.ReindexIfEmpty(ctx, videoClient, indexClient); err != nil {
					logger.L().Warn("回填检索索引失败，稍后重试", zap.Error(err))
				} else if err := indexConsumer.Run(ctx); err != nil {
					logger.L().Warn("视频索引事件消费者退出，稍后重连", zap.Error(err))
				} else {
					return // ctx 取消，正常退出
				}

				select {
				case <-ctx.Done():
					return
				case <-time.After(indexRetryInterval):
				}
			}
		}()
	}

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

	logger.L().Info("搜索服务已退出")
}
