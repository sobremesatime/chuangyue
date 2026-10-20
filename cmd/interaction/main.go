// Command interaction 是互动服务的独立进程入口。
//
// 它对外提供互动域的 gRPC 契约（点赞 / 收藏 / 历史、评论、弹幕与站内通知），
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	interactiondto "github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/handler"
	interactionrepo "github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/grpcclient"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/mq"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// shutdownTimeout 是收到退出信号后等待在途 RPC 完成的最长时间。
const shutdownTimeout = 10 * time.Second

// purgeConsumerRetryInterval 是视频删除事件消费者重连前的等待时长。
const purgeConsumerRetryInterval = 5 * time.Second

func main() {
	configPath := flag.String("c", "", "配置文件路径，留空则按默认顺序查找")
	addr := flag.String("addr", ":9101", "gRPC 监听地址")
	advertise := flag.String("advertise", "127.0.0.1:9101", "注册到 Etcd 的对外地址")
	serviceName := flag.String("service", "interaction", "注册到 Etcd 的服务名")
	metricsAddr := flag.String("metrics-addr", ":9201", "Prometheus 指标监听地址")
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

	// 互动域自本次起成为互动表（点赞 / 收藏 / 评论 / 弹幕 / 通知）的唯一写入方，
	// 相应行 ID 都在本进程生成；显式初始化雪花节点（单机固定 nodeID=1，集群需按实例分配）。
	if err := snowflake.Init(1); err != nil {
		logger.L().Fatal("初始化雪花 ID 失败", zap.Error(err))
	}

	// 物理拆库：互动域连自己的库（InteractiveMySQL），不再触达视频库。
	// 配置未单独指定时回退主库（单库开发模式），见 config.Normalize。
	db, err := database.NewMySQL(cfg.InteractionMySQL)
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

	// 注册解析器后，客户端才能用 etcd:///user 按服务名拨号到用户服务。
	registry.RegisterResolver(reg)

	// 互动域取用户展示信息的端口：独立部署后改走用户服务的 gRPC，不再本地 JOIN user。
	userConn, err := grpcclient.Dial("user")
	if err != nil {
		logger.L().Fatal("建立用户服务连接失败", zap.Error(err))
	}
	defer func() { _ = userConn.Close() }()
	userProvider := newUserProvider(userv1.NewUserServiceClient(userConn))

	// 互动域取视频信息的端口：拆库后不再直读视频表，存在性校验与卡片字段都走视频服务。
	videoConn, err := grpcclient.Dial("video")
	if err != nil {
		logger.L().Fatal("建立视频服务连接失败", zap.Error(err))
	}
	defer func() { _ = videoConn.Close() }()
	videoProvider := newVideoProvider(videov1.NewVideoServiceClient(videoConn))

	// 计数回写生产者：把点赞 / 收藏计数快照发到 video.stat，由视频域异步刷新只读副本。
	// kafka.Writer 惰性连接，Kafka 暂时不可用不影响本服务启动。
	statProducer := mq.NewProducer(cfg.Kafka.Brokers, cfg.Kafka.StatTopic)
	defer func() { _ = statProducer.Close() }()
	statPublisher := newStatPublisher(statProducer)

	repo := interactionrepo.New(db)
	stats := service.NewStatsService(repo)
	signals := service.NewSignalService(repo)
	// 评论 / 弹幕 / 通知 / 互动四类能力迁入本进程后，装配它们所需的 service：
	// 通知服务先构造，评论服务持有它用于点赞 / 评论 / 回复时写站内通知。
	notificationSvc := service.NewNotificationService(repo, userProvider, videoProvider)
	commentSvc := service.NewCommentService(repo, notificationSvc, userProvider, videoProvider)
	danmakuSvc := service.NewDanmakuService(repo, notificationSvc, userProvider, videoProvider)
	interactionSvc := service.NewInteractionService(repo, videoProvider, statPublisher)

	// 视频删除事件消费者：视频域删除视频后投递 chuangyue.video.deleted，
	// 本服务据此清理该视频的评论 / 弹幕 / 点赞 / 收藏 / 历史与通知（见 service.PurgeService）。
	// 与计数回写一样，Kafka 未就绪不影响互动服务对外提供能力。
	purgeSvc := service.NewPurgeService(repo)
	purgeConsumer := mq.NewConsumer(cfg.Kafka.Brokers, cfg.Kafka.DeleteTopic, cfg.Kafka.DeleteGroup, purgeSvc.HandleVideoDeleted)
	defer func() { _ = purgeConsumer.Close() }()

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
	interactionv1.RegisterInteractionServiceServer(server, handler.NewGRPCInteractionServer(stats, signals))
	interactionv1.RegisterCommentServiceServer(server, handler.NewGRPCCommentServer(commentSvc))
	interactionv1.RegisterDanmakuServiceServer(server, handler.NewGRPCDanmakuServer(danmakuSvc))
	interactionv1.RegisterUserInteractionServiceServer(server, handler.NewGRPCUserInteractionServer(interactionSvc))
	interactionv1.RegisterNotificationServiceServer(server, handler.NewGRPCNotificationServer(notificationSvc))

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

	logger.L().Info("互动服务启动",
		zap.String("service", *serviceName),
		zap.String("addr", lis.Addr().String()),
		zap.String("advertise", *advertise),
	)

	go func() {
		if err := server.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.L().Fatal("gRPC 服务异常退出", zap.Error(err))
		}
	}()

	// 视频删除事件消费者：先确保 topic 存在，再持续拉取；失败退避重连。
	//
	// 与视频域的计数回写消费者同构：Run 在拉取失败时会返回，而这是长驻进程——
	// 只跑一次就放弃的话，一次 Kafka 抖动就会让「删视频清互动」永久停摆。
	go func() {
		for {
			if err := mq.EnsureTopic(cfg.Kafka.Brokers, cfg.Kafka.DeleteTopic, 3); err != nil {
				logger.L().Warn("确保视频删除 topic 失败，稍后重试", zap.Error(err))
			} else if err := purgeConsumer.Run(ctx); err != nil {
				logger.L().Warn("视频删除事件消费者退出，稍后重连", zap.Error(err))
			} else {
				return // ctx 取消，正常退出
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(purgeConsumerRetryInterval):
			}
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

	logger.L().Info("互动服务已退出")
}

// userProvider 是基于用户服务 gRPC 契约的 UserProvider 实现。
//
// 它只在互动服务独立部署后使用：把 user.v1.UserService 返回的 UserBrief
// 转成互动域所需的最小结构，从而让互动域彻底摆脱对 `user` 表的直读。
type userProvider struct {
	client userv1.UserServiceClient
}

// newUserProvider 构造基于 gRPC 的用户信息提供者。
func newUserProvider(client userv1.UserServiceClient) *userProvider {
	return &userProvider{client: client}
}

// GetUsers 批量取用户简要信息并转成 map。
func (p *userProvider) GetUsers(ctx context.Context, ids []int64) (map[int64]service.UserBrief, error) {
	resp, err := p.client.GetUsers(ctx, &userv1.GetUsersRequest{Ids: ids})
	if err != nil {
		return nil, err
	}

	out := make(map[int64]service.UserBrief, len(resp.GetUsers()))
	for _, u := range resp.GetUsers() {
		out[u.GetId()] = service.UserBrief{
			ID:       u.GetId(),
			Username: u.GetUsername(),
			Nickname: u.GetNickname(),
			Avatar:   u.GetAvatar(),
		}
	}
	return out, nil
}

// videoProvider 是基于视频服务 gRPC 契约的 VideoProvider 实现。
//
// 拆库后互动域不再拥有视频表：存在性校验、卡片字段、视频标题都经此向视频服务索取。
// 视频不存在时 GetVideo 返回 (nil, nil)，由 service 层决定如何报错。
type videoProvider struct {
	client videov1.VideoServiceClient
}

// newVideoProvider 构造基于 gRPC 的视频信息提供者。
func newVideoProvider(client videov1.VideoServiceClient) *videoProvider {
	return &videoProvider{client: client}
}

// GetVideo 查单个视频；视频不存在（NotFound）时返回 (nil, nil)。
func (p *videoProvider) GetVideo(ctx context.Context, id int64) (*interactiondto.VideoInfo, error) {
	resp, err := p.client.GetVideo(ctx, &videov1.GetVideoRequest{Id: id})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	info := toInteractionVideoInfo(resp.GetVideo())
	return &info, nil
}

// GetVideos 批量取视频信息并转成 map；缺失的 id 不出现在结果里。
func (p *videoProvider) GetVideos(ctx context.Context, ids []int64) (map[int64]interactiondto.VideoInfo, error) {
	resp, err := p.client.GetVideoInfos(ctx, &videov1.GetVideoInfosRequest{Ids: ids})
	if err != nil {
		return nil, err
	}

	out := make(map[int64]interactiondto.VideoInfo, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		out[v.GetId()] = toInteractionVideoInfo(v)
	}
	return out, nil
}

// toInteractionVideoInfo 把视频服务契约映射为互动域内部使用的视频信息结构。
func toInteractionVideoInfo(v *videov1.VideoInfo) interactiondto.VideoInfo {
	info := interactiondto.VideoInfo{
		ID:          v.GetId(),
		Title:       v.GetTitle(),
		Description: v.GetDescription(),
		CoverURL:    v.GetCoverUrl(),
		PlayURL:     v.GetPlayUrl(),
		Duration:    int(v.GetDuration()),
		CategoryID:  v.GetCategoryId(),
		UploaderID:  v.GetUploaderId(),
		ViewCount:   v.GetViewCount(),
		Status:      int8(v.GetStatus()),
		ErrorMsg:    v.GetErrorMsg(),
	}
	if ts := v.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}

// statPublisher 把计数快照投递到 video.stat 主题。
type statPublisher struct {
	producer *mq.Producer
}

// newStatPublisher 构造基于 Kafka 生产者的计数发布者。
func newStatPublisher(producer *mq.Producer) *statPublisher {
	return &statPublisher{producer: producer}
}

// PublishVideoStat 以 video_id 为分区键投递，保证同一视频的计数变更在分区内有序。
func (p *statPublisher) PublishVideoStat(ctx context.Context, stat videodto.VideoStatChanged) error {
	return p.producer.Publish(ctx, strconv.FormatInt(stat.VideoID, 10), stat)
}
