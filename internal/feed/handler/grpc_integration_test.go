//go:build integration

// 首页推荐服务 gRPC 入口的集成测试：需要本地 MySQL、Redis 与 Etcd。
//
// feed 是聚合者：本测试在同一进程内起 video / interaction / feed 三个 gRPC server，
// 把各自的真实监听端口注册到 Etcd，再由 feed 的两个客户端经 Etcd 发现拨号，
// 覆盖「实例注册 -> 跨服务发现 -> 个性化重排」的完整链路。依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/feed/handler/...
package handler

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"

	feedv1 "github.com/sobremesatime/chuangyue/api/gen/feed/v1"
	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	feedservice "github.com/sobremesatime/chuangyue/internal/feed/service"
	interactionhandler "github.com/sobremesatime/chuangyue/internal/interaction/handler"
	interactionmodel "github.com/sobremesatime/chuangyue/internal/interaction/model"
	interactionrepo "github.com/sobremesatime/chuangyue/internal/interaction/repository"
	interactionsvc "github.com/sobremesatime/chuangyue/internal/interaction/service"
	videohandler "github.com/sobremesatime/chuangyue/internal/video/handler"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	videosvc "github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/cache"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// newTestDB 自包含地连本地 MySQL（DSN 各段可用环境变量覆盖），连不上直接跳过。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	cfg := config.MySQLConfig{
		Host:         envOr("MYSQL_HOST", "127.0.0.1"),
		Port:         envIntOr("MYSQL_PORT", 3306),
		Username:     envOr("MYSQL_USER", "root"),
		Password:     envOr("MYSQL_PASSWORD", "root123456"),
		Database:     envOr("MYSQL_DATABASE", "chuangyue"),
		Charset:      "utf8mb4",
		MaxIdleConns: 2,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	}
	db, err := database.NewMySQL(cfg)
	if err != nil {
		t.Skipf("MySQL 不可用，跳过集成测试: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

// newTestRegistry 连本地 Etcd，连不上直接跳过。
func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()

	reg, err := registry.New([]string{envOr("ETCD_ENDPOINTS", "127.0.0.1:2379")})
	if err != nil {
		t.Skipf("Etcd 不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

// newTestRedis 连本地 Redis（连接参数可用环境变量覆盖），连不上直接跳过。
// 默认值与 configs/dev/config.yaml 保持一致。
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	rdb, err := cache.NewRedis(config.RedisConfig{
		Host:     envOr("REDIS_HOST", "127.0.0.1"),
		Port:     envIntOr("REDIS_PORT", 6379),
		Password: envOr("REDIS_PASSWORD", ""),
		DB:       envIntOr("REDIS_DB", 0),
		PoolSize: 5,
	})
	if err != nil {
		t.Skipf("Redis 不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// uniqueTestService 生成唯一服务名，避免历史残留实例互相干扰。
func uniqueTestService(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// serveAndRegister 在随机端口上启动一个 gRPC server 并以指定服务名注册到 Etcd，
// 返回拨号地址（etcd:///<service>）。
func serveAndRegister(t *testing.T, reg *registry.Registry, srv *grpc.Server, register func(*grpc.Server), servicePrefix string) string {
	t.Helper()

	register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	serviceName := uniqueTestService(servicePrefix)
	deregister, err := reg.Register(context.Background(), serviceName, lis.Addr().String())
	if err != nil {
		t.Fatalf("注册服务实例失败: %v", err)
	}
	t.Cleanup(deregister)
	return "etcd:///" + serviceName
}

// hasPublishedVideo 判断库里是否有已上架视频；没有则后续断言无从谈起。
func hasPublishedVideo(t *testing.T, db *gorm.DB) bool {
	t.Helper()

	var count int64
	if err := db.Model(&videomodel.Video{}).Where("status = ?", videomodel.StatusPublished).Count(&count).Error; err != nil {
		t.Fatalf("查询已上架视频失败: %v", err)
	}
	return count > 0
}

// videoIDSet 把视频列表的 id 收成集合，便于判定「是否包含」。
func videoIDSet(videos []*videov1.VideoBrief) map[int64]struct{} {
	set := make(map[int64]struct{}, len(videos))
	for _, v := range videos {
		set[v.GetId()] = struct{}{}
	}
	return set
}

// TestGRPCFeedServerViaEtcd 覆盖首页推荐服务经 Etcd 发现的完整调用链路及个性化规则。
func TestGRPCFeedServerViaEtcd(t *testing.T) {
	db := newTestDB(t)
	reg := newTestRegistry(t)
	rdb := newTestRedis(t)
	registry.RegisterResolver(reg)

	// video server（真实 MySQL）
	videoAddr := serveAndRegister(t, reg, grpc.NewServer(
		grpc.ChainUnaryInterceptor(identity.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(identity.StreamServerInterceptor()),
	),
		func(s *grpc.Server) {
			videov1.RegisterVideoServiceServer(s, videohandler.NewGRPCVideoServer(videosvc.New(videorepo.New(db), nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, nil)))
		}, "it-video")

	// interaction server（真实 MySQL）
	interactionAddr := serveAndRegister(t, reg, grpc.NewServer(
		grpc.ChainUnaryInterceptor(identity.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(identity.StreamServerInterceptor()),
	),
		func(s *grpc.Server) {
			irepo := interactionrepo.New(db)
			interactionv1.RegisterInteractionServiceServer(s, interactionhandler.NewGRPCInteractionServer(
				interactionsvc.NewStatsService(irepo),
				interactionsvc.NewSignalService(irepo),
			))
		}, "it-interaction")

	// feed 的两个客户端连接（挂身份客户端拦截器，把 ctx 里的身份透传给下游）
	videoConn, err := grpc.NewClient(videoAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(identity.UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(identity.StreamClientInterceptor()),
	)
	if err != nil {
		t.Fatalf("拨号视频服务失败: %v", err)
	}
	t.Cleanup(func() { _ = videoConn.Close() })

	interactionConn, err := grpc.NewClient(interactionAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(identity.UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(identity.StreamClientInterceptor()),
	)
	if err != nil {
		t.Fatalf("拨号互动服务失败: %v", err)
	}
	t.Cleanup(func() { _ = interactionConn.Close() })

	// feed server（自身用 Redis + 两个客户端）
	feedAddr := serveAndRegister(t, reg, grpc.NewServer(
		grpc.ChainUnaryInterceptor(identity.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(identity.StreamServerInterceptor()),
	),
		func(s *grpc.Server) {
			svc := feedservice.New(videov1.NewVideoServiceClient(videoConn), interactionv1.NewInteractionServiceClient(interactionConn), rdb)
			feedv1.RegisterFeedServiceServer(s, NewGRPCFeedServer(svc))
		}, "it-feed")

	feedConn, err := grpc.NewClient(feedAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(identity.UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(identity.StreamClientInterceptor()),
	)
	if err != nil {
		t.Fatalf("拨号首页服务失败: %v", err)
	}
	t.Cleanup(func() { _ = feedConn.Close() })

	client := feedv1.NewFeedServiceClient(feedConn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// ① 未登录：能拿到非空结果且 total > 0
	var guestTop []*videov1.VideoBrief
	t.Run("guest", func(t *testing.T) {
		if !hasPublishedVideo(t, db) {
			t.Skip("库里没有已上架视频，跳过")
		}
		resp, err := client.GetHomeFeed(ctx, &feedv1.GetHomeFeedRequest{Page: 1, PageSize: 100})
		if err != nil {
			t.Fatalf("GetHomeFeed(未登录) 失败: %v", err)
		}
		if resp.GetTotal() <= 0 {
			t.Fatalf("未登录 total 应 > 0, 实际 %d", resp.GetTotal())
		}
		if len(resp.GetVideos()) == 0 {
			t.Fatalf("未登录应返回非空结果")
		}
		guestTop = resp.GetVideos()
	})

	// ② 登录用户：把「未登录首页热度最高的若干视频」记为该用户看过的，
	//    这些视频必在候选池内，若排除逻辑失效它们一定会出现在首页，断言才有意义。
	t.Run("logged_in_excludes_watched", func(t *testing.T) {
		if len(guestTop) == 0 {
			t.Skip("没有可用的候选视频，跳过")
		}

		// 用唯一 user_id 避免污染其它测试数据；只删自己插入的行。
		userID := time.Now().UnixNano()
		watched := make([]int64, 0, len(guestTop))
		for _, v := range guestTop {
			watched = append(watched, v.GetId())
		}
		for _, vid := range watched {
			if err := db.Create(&interactionmodel.VideoWatchHistory{
				ID:       snowflake.NextID(),
				UserID:   userID,
				VideoID:  vid,
				Progress: 1,
			}).Error; err != nil {
				t.Fatalf("插入观看记录失败: %v", err)
			}
		}
		t.Cleanup(func() {
			db.Where("user_id = ?", userID).Delete(&interactionmodel.VideoWatchHistory{})
		})

		// 身份经 ctx 透传（客户端拦截器写入 metadata，服务端拦截器取回），不再放进请求字段。
		userCtx := identity.WithUser(ctx, identity.User{ID: userID, Role: identity.RoleUser})
		resp, err := client.GetHomeFeed(userCtx, &feedv1.GetHomeFeedRequest{Page: 1, PageSize: 100})
		if err != nil {
			t.Fatalf("GetHomeFeed(登录) 失败: %v", err)
		}

		got := videoIDSet(resp.GetVideos())
		for _, vid := range watched {
			if _, ok := got[vid]; ok {
				t.Fatalf("首页包含了看过的视频 %d（排除规则未生效）", vid)
			}
		}
	})

	// ③ 参数宽容性：page_size=0 与超大值都不报错、能返回结果
	t.Run("lenient_paging", func(t *testing.T) {
		if !hasPublishedVideo(t, db) {
			t.Skip("库里没有已上架视频，跳过")
		}
		for _, pageSize := range []int32{0, 99999} {
			resp, err := client.GetHomeFeed(ctx, &feedv1.GetHomeFeedRequest{Page: 1, PageSize: pageSize})
			if err != nil {
				t.Fatalf("GetHomeFeed(page_size=%d) 不应报错: %v", pageSize, err)
			}
			if resp.GetTotal() < 0 {
				t.Fatalf("GetHomeFeed(page_size=%d) total 非法: %d", pageSize, resp.GetTotal())
			}
		}
	})
}
