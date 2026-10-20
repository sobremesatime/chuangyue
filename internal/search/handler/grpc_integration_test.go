//go:build integration

// 搜索服务 gRPC 入口的集成测试：需要本地 MySQL 与 Etcd。
//
// search 是聚合者：它自己不持有业务表，命中的视频来自视频服务的检索接口。本测试在同一
// 进程内起 video / search 两个真实 gRPC server，把各自的真实监听端口注册到 Etcd，再由
// search 的客户端经 Etcd 发现拨号，覆盖「实例注册 -> 跨服务发现 -> 关键词与分类过滤」的
// 完整链路。依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/search/handler/...
package handler

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"

	searchv1 "github.com/sobremesatime/chuangyue/api/gen/search/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	searchservice "github.com/sobremesatime/chuangyue/internal/search/service"
	videohandler "github.com/sobremesatime/chuangyue/internal/video/handler"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	videosvc "github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
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

// 测试专用的分类 ID：取值远离业务分类（1~5），避免与真实数据互相命中。
const (
	testCategoryID  int64 = 991
	otherCategoryID int64 = 992
	testDuration    int32 = 137
	testViewCount   int64 = 42
	testVideoCount        = 2
)

// seedSearchFixtures 插入两条第 marker 标题的已上架视频（同一测试分类），返回它们的 id。
//
// marker 每次运行唯一，因此「按 marker 检索」的结果集必然只包含这两条，断言不受库里既有
// 数据影响；清理时也只删自己插入的这两行。
func seedSearchFixtures(t *testing.T, db *gorm.DB, marker string) []int64 {
	t.Helper()

	ids := make([]int64, 0, testVideoCount)
	for i := range testVideoCount {
		id := snowflake.NextID()
		if err := db.Create(&videomodel.Video{
			ID:         id,
			Title:      fmt.Sprintf("%s-%d", marker, i),
			Status:     videomodel.StatusPublished,
			CategoryID: testCategoryID,
			Duration:   int(testDuration),
			ViewCount:  testViewCount,
		}).Error; err != nil {
			t.Fatalf("插入临时视频失败: %v", err)
		}
		ids = append(ids, id)
	}
	// 只删自己插入的行。
	t.Cleanup(func() { db.Where("id IN ?", ids).Delete(&videomodel.Video{}) })
	return ids
}

// TestGRPCSearchServerViaEtcd 覆盖搜索服务经 Etcd 发现的完整调用链路及过滤语义。
func TestGRPCSearchServerViaEtcd(t *testing.T) {
	db := newTestDB(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	// video server（真实 MySQL，供 search 取数）
	videoAddr := serveAndRegister(t, reg, grpc.NewServer(),
		func(s *grpc.Server) {
			videov1.RegisterVideoServiceServer(s, videohandler.NewGRPCVideoServer(videosvc.New(videorepo.New(db), nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, nil)))
		}, "it-video")

	// search 的客户端连接：经上面注册的服务名发现视频服务。
	videoConn, err := grpc.NewClient(videoAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("拨号视频服务失败: %v", err)
	}
	t.Cleanup(func() { _ = videoConn.Close() })

	// search server（自身只用 video 客户端，不连库）
	searchAddr := serveAndRegister(t, reg, grpc.NewServer(),
		func(s *grpc.Server) {
			searchv1.RegisterSearchServiceServer(s, NewGRPCSearchServer(
				searchservice.New(videov1.NewVideoServiceClient(videoConn)),
			))
		}, "it-search")

	searchConn, err := grpc.NewClient(searchAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("拨号搜索服务失败: %v", err)
	}
	t.Cleanup(func() { _ = searchConn.Close() })

	client := searchv1.NewSearchServiceClient(searchConn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	marker := fmt.Sprintf("itsearch%d", time.Now().UnixNano())
	ids := seedSearchFixtures(t, db, marker)

	// ① 关键词命中：结果只包含自己插入的几条，且带卡片字段（duration / view_count）。
	t.Run("keyword_match", func(t *testing.T) {
		resp, err := client.SearchVideos(ctx, &searchv1.SearchVideosRequest{Keyword: marker, Page: 1, PageSize: 100})
		if err != nil {
			t.Fatalf("SearchVideos(关键词) 失败: %v", err)
		}
		if resp.GetTotal() != int64(len(ids)) {
			t.Fatalf("关键词应命中 %d 条, 实际 total=%d", len(ids), resp.GetTotal())
		}

		got := make(map[int64]*videov1.VideoBrief, len(resp.GetVideos()))
		for _, v := range resp.GetVideos() {
			got[v.GetId()] = v
		}
		for _, id := range ids {
			v, ok := got[id]
			if !ok {
				t.Fatalf("结果未包含临时视频 %d", id)
			}
			if v.GetDuration() != testDuration || v.GetViewCount() != testViewCount {
				t.Fatalf("卡片字段应透传, 实际 duration=%d view_count=%d", v.GetDuration(), v.GetViewCount())
			}
			if v.GetCategoryId() != testCategoryID {
				t.Fatalf("分类应为 %d, 实际 %d", testCategoryID, v.GetCategoryId())
			}
		}
	})

	// ② 分类过滤与关键词是「与」关系：命中分类仍返回全部，未命中分类必须为 0。
	t.Run("category_is_and", func(t *testing.T) {
		hit, err := client.SearchVideos(ctx, &searchv1.SearchVideosRequest{
			Keyword: marker, CategoryId: testCategoryID, Page: 1, PageSize: 100,
		})
		if err != nil {
			t.Fatalf("SearchVideos(关键词+命中分类) 失败: %v", err)
		}
		if hit.GetTotal() != int64(len(ids)) {
			t.Fatalf("命中分类时 total 应为 %d, 实际 %d", len(ids), hit.GetTotal())
		}

		miss, err := client.SearchVideos(ctx, &searchv1.SearchVideosRequest{
			Keyword: marker, CategoryId: otherCategoryID, Page: 1, PageSize: 100,
		})
		if err != nil {
			t.Fatalf("SearchVideos(关键词+未命中分类) 失败: %v", err)
		}
		if miss.GetTotal() != 0 || len(miss.GetVideos()) != 0 {
			t.Fatalf("未命中分类应返回空结果, 实际 total=%d len=%d", miss.GetTotal(), len(miss.GetVideos()))
		}
	})

	// ③ 分页：total 仍是命中总数，单页条数受 page_size 约束。
	t.Run("pagination", func(t *testing.T) {
		resp, err := client.SearchVideos(ctx, &searchv1.SearchVideosRequest{Keyword: marker, Page: 1, PageSize: 1})
		if err != nil {
			t.Fatalf("SearchVideos(分页) 失败: %v", err)
		}
		if len(resp.GetVideos()) != 1 {
			t.Fatalf("page_size=1 应返回 1 条, 实际 %d", len(resp.GetVideos()))
		}
		if resp.GetTotal() != int64(len(ids)) {
			t.Fatalf("分页时 total 应仍为 %d, 实际 %d", len(ids), resp.GetTotal())
		}
	})

	// ④ 无命中：返回空结果而不是报错（检索接口对参数宽容）。
	t.Run("no_match_is_empty", func(t *testing.T) {
		resp, err := client.SearchVideos(ctx, &searchv1.SearchVideosRequest{Keyword: marker + "-不存在", Page: 1, PageSize: 100})
		if err != nil {
			t.Fatalf("SearchVideos(无命中) 不应报错: %v", err)
		}
		if resp.GetTotal() != 0 || len(resp.GetVideos()) != 0 {
			t.Fatalf("无命中应返回空结果, 实际 total=%d len=%d", resp.GetTotal(), len(resp.GetVideos()))
		}
	})
}
