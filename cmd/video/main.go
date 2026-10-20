// Command video 是视频服务的独立进程入口。
//
// 它对外提供视频域的 gRPC 契约（视频查询、分片上传与举报），
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
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/handler"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/cache"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/cron"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/mq"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/storage"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// shutdownTimeout 是收到退出信号后等待在途 RPC 完成的最长时间。
const shutdownTimeout = 10 * time.Second

// statConsumerRetryInterval 是计数回写消费者重连前的等待时长。
const statConsumerRetryInterval = 5 * time.Second

func main() {
	configPath := flag.String("c", "", "配置文件路径，留空则按默认顺序查找")
	addr := flag.String("addr", ":9102", "gRPC 监听地址")
	advertise := flag.String("advertise", "127.0.0.1:9102", "注册到 Etcd 的对外地址")
	serviceName := flag.String("service", "video", "注册到 Etcd 的服务名")
	metricsAddr := flag.String("metrics-addr", ":9202", "Prometheus 指标监听地址")
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

	db, err := database.NewMySQL(cfg.MySQL)
	if err != nil {
		logger.L().Fatal("初始化 MySQL 失败", zap.Error(err))
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}

	reg, err := registry.New(cfg.Etcd.Endpoints)
	if err != nil {
		logger.L().Fatal("初始化服务注册中心失败", zap.Error(err))
	}
	defer func() { _ = reg.Close() }()

	repo := videorepo.New(db)

	// 对象存储：视频源文件、封面与分片上传都经此抽象访问，当前版本为本地磁盘驱动。
	store, err := storage.New(cfg.Upload)
	if err != nil {
		logger.L().Fatal("初始化存储驱动失败", zap.Error(err))
	}

	// 转码任务生产者：kafka.Writer 惰性连接，Kafka 暂时不可用也不影响服务启动，
	// 投递失败会体现在上传接口的报错上。
	producer := mq.NewProducer(cfg.Kafka.Brokers, cfg.Kafka.Topic)
	defer func() { _ = producer.Close() }()

	// 视频删除事件生产者：另一个 topic，因此单独一个实例（mq.Producer 绑定单个 topic）。
	// 同样惰性连接；投递失败只记日志，不影响删除结果（见 service.Delete）。
	deleteProducer := mq.NewProducer(cfg.Kafka.Brokers, cfg.Kafka.DeleteTopic)
	defer func() { _ = deleteProducer.Close() }()

	// 检索索引事件生产者：上架 / 编辑 / 下架 / 删除 / 转码完成时通知搜索域更新索引。
	// 同样惰性连接：Kafka 不可用只让索引滞后，不影响视频域自身的读写。
	indexProducer := mq.NewProducer(cfg.Kafka.Brokers, cfg.Kafka.IndexTopic)
	defer func() { _ = indexProducer.Close() }()

	// 过期上传清理任务的单飞锁需要 Redis。与网关限流一致：Redis 不可用**不阻断启动**，
	// 只停用单飞能力，退化为「多副本各跑一轮」——重复执行幂等，无正确性影响。
	rdb, err := cache.NewRedis(cfg.Redis)
	if err != nil {
		logger.L().Warn("Redis 不可用，上传清理任务的单飞锁已停用", zap.Error(err))
	} else {
		defer func() { _ = rdb.Close() }()
	}

	svc := service.New(repo, store, cfg.Upload, cfg.Transcode, producer, rdb,
		service.WithDeletePublisher(&deletePublisher{producer: deleteProducer}),
		service.WithIndexPublisher(&indexPublisher{producer: indexProducer}))
	reportSvc := service.NewReportService(repo)

	// 互动计数回写消费者：消费 video.stat 主题，把互动域的点赞 / 收藏计数快照
	// 刷进视频库的计数副本（只供热度排序）。Kafka 未就绪时消费者会失败退避，
	// 不影响视频服务对外提供服务——计数副本短暂停更即可，真源仍在互动域。
	statConsumer := mq.NewConsumer(cfg.Kafka.Brokers, cfg.Kafka.StatTopic, cfg.Kafka.StatGroup, svc.ApplyVideoStat)
	defer func() { _ = statConsumer.Close() }()

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
	videov1.RegisterVideoServiceServer(server, handler.NewGRPCVideoServer(svc))
	videov1.RegisterUploadServiceServer(server, handler.NewGRPCUploadServer(svc))
	videov1.RegisterReportServiceServer(server, handler.NewGRPCReportServer(reportSvc))

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

	// 周期任务（配置项 jobs.enabled 可整体关闭）：过期上传清理 + 播放量批量落库。
	//
	// 两者都不在调度器层面加单飞锁，原因各异，都由任务本身兜住：
	//   - 上传清理自带 pkg/lock 的单飞（见 service.CleanupUploads），沿用原有口径；
	//   - 播放量落库的「取走增量」是 Redis 脚本里的原子 HGETALL+DEL，并发执行只会把
	//     工作量拆开，不会重复计数——锁在这里是多余的开销。
	// 两者都声明 WithRunOnStart：清理要立刻回收上次进程遗留的过期会话，落库则让
	// 上一个进程异常退出时残留在 Redis 的增量尽快落回库。
	var sched *cron.Scheduler
	if cfg.Jobs.Enabled {
		sched = cron.New(rdb)
		if err := sched.Add("video-upload-cleanup", cfg.Jobs.UploadCleanup, svc.CleanupUploads,
			cron.WithRunOnStart()); err != nil {
			logger.L().Warn("注册过期上传清理任务失败", zap.Error(err))
		}
		if err := sched.Add("video-view-count-flush", cfg.Jobs.ViewCountFlush, svc.FlushViewCounts,
			cron.WithRunOnStart()); err != nil {
			logger.L().Warn("注册播放量批量落库任务失败", zap.Error(err))
		}
		sched.Start(ctx)
	}

	// 计数回写消费者：先确保 topic 存在，再持续拉取。
	//
	// 用「失败退避重连」包住 Run：Run 在拉取失败（如 Kafka 尚未就绪 / 短暂抖动）时会返回，
	// 而视频服务是长驻进程——若只跑一次就放弃，计数副本会一直停更到下次重启。
	// 这里循环重连，任一环节失败都只记日志，不阻断视频服务自身。
	go func() {
		for {
			if err := mq.EnsureTopic(cfg.Kafka.Brokers, cfg.Kafka.StatTopic, 3); err != nil {
				logger.L().Warn("确保互动计数 topic 失败，稍后重试", zap.Error(err))
			} else if err := statConsumer.Run(ctx); err != nil {
				logger.L().Warn("互动计数回写消费者退出，稍后重连", zap.Error(err))
			} else {
				return // ctx 取消，正常退出
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(statConsumerRetryInterval):
			}
		}
	}()

	deregister, err := reg.Register(ctx, *serviceName, *advertise)
	if err != nil {
		logger.L().Fatal("注册服务实例失败", zap.String("service", *serviceName), zap.Error(err))
	}
	defer deregister()

	logger.L().Info("视频服务启动",
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

	logger.L().Info("视频服务已退出")
}

// deletePublisher 把「视频已删除」事件投递到 chuangyue.video.deleted。
//
// 以 video_id 作分区键：同一视频的事件落在同一分区，天然有序；
// 消费侧按 video_id 批量删除，重复投递也安全。
type deletePublisher struct {
	producer *mq.Producer
}

// PublishVideoDeleted 实现 service.DeletePublisher。
func (p *deletePublisher) PublishVideoDeleted(ctx context.Context, videoID int64) error {
	return p.producer.Publish(ctx, strconv.FormatInt(videoID, 10), videodto.VideoDeleted{VideoID: videoID})
}

// indexPublisher 把视频索引事件投递到 chuangyue.video.index。
//
// 以 video_id 作分区键：同一视频的 upsert / delete 落在同一分区，消费顺序与产生顺序一致，
// 避免「先上架后下架」被乱序消费成「先下架后上架」。
type indexPublisher struct {
	producer *mq.Producer
}

// PublishVideoIndex 实现 service.IndexPublisher。
func (p *indexPublisher) PublishVideoIndex(ctx context.Context, event videodto.VideoIndexEvent) error {
	key := event.VideoID
	if key == 0 && event.Video != nil {
		key = event.Video.ID
	}
	return p.producer.Publish(ctx, strconv.FormatInt(key, 10), event)
}
