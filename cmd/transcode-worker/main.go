// Command transcode-worker 是异步转码流水线的消费者。
//
// 它从 Kafka 拉取转码任务，调用 FFmpeg 把源文件转成多码率 HLS，
// 再经视频服务把播放地址、时长与状态回写。与网关是两个独立进程：
// 网关负责接收上传，Worker 负责耗时的转码，两者通过消息队列解耦。
//
// 本进程不持有任何业务表：源文件在本地磁盘 / 对象存储（经 storage 抽象读取），
// 视频状态的读写统一经视频服务的 gRPC 接口（经 Etcd 按服务名发现）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	transcodesvc "github.com/sobremesatime/chuangyue/internal/transcode/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/grpcclient"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/mq"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/storage"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

func main() {
	configPath := flag.String("c", "", "配置文件路径，留空则按默认顺序查找")
	metricsAddr := flag.String("metrics-addr", ":9206", "Prometheus 指标监听地址")
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

	// Worker 不再直连业务库：转码状态的读写统一经视频服务的 gRPC 接口。
	videoConn, err := grpcclient.Dial("video")
	if err != nil {
		logger.L().Fatal("建立视频服务连接失败", zap.Error(err))
	}
	defer func() { _ = videoConn.Close() }()

	// 先建好主题再消费：消费组在 topic 不存在时加入会拿不到分区
	if err := mq.EnsureTopic(cfg.Kafka.Brokers, cfg.Kafka.Topic, 1); err != nil {
		logger.L().Fatal("初始化转码主题失败", zap.Error(err))
	}

	// 存储驱动：用于把源文件取到本地（本地磁盘直读，对象存储则下载到临时文件）
	store, err := storage.New(cfg.Upload)
	if err != nil {
		logger.L().Fatal("初始化存储驱动失败", zap.Error(err))
	}

	worker := transcodesvc.NewWorker(
		cfg.Upload,
		cfg.Transcode,
		videov1.NewVideoServiceClient(videoConn),
		transcodesvc.NewTranscoder(cfg.Transcode),
		store,
	)
	consumer := mq.NewConsumer(cfg.Kafka.Brokers, cfg.Kafka.Topic, cfg.Kafka.Group, worker.Handle)
	defer func() { _ = consumer.Close() }()

	// 收到退出信号后停止拉取新任务，正在处理的一条会跑完
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 链路追踪：转码任务经 Kafka 异步投递、已不在原请求链路内，
	// 这里的 span 主要用于观察 Worker 自身耗时与它对视频服务的回写调用。
	if err := tracing.Setup(ctx, "transcode-worker", cfg.Tracing.Endpoint, cfg.Tracing.SampleRatio); err != nil {
		logger.L().Warn("初始化链路追踪失败，追踪不可用", zap.Error(err))
	}

	// Prometheus 指标服务：与主服务同生命周期（ctx 取消即关闭）。
	// 监听失败只告警不致命——指标不该成为主服务能不能启动的前提。
	if err := metrics.Default().StartServer(ctx, *metricsAddr); err != nil {
		logger.L().Warn("启动 metrics 服务失败，指标不可抓取", zap.String("addr", *metricsAddr), zap.Error(err))
	}

	logger.L().Info("转码 Worker 启动",
		zap.String("topic", cfg.Kafka.Topic),
		zap.String("group", cfg.Kafka.Group),
		zap.Strings("brokers", cfg.Kafka.Brokers),
		zap.String("output_dir", cfg.Transcode.OutputDir),
	)

	if err := consumer.Run(ctx); err != nil {
		logger.L().Fatal("转码 Worker 异常退出", zap.Error(err))
	}
	logger.L().Info("转码 Worker 已退出")
}
