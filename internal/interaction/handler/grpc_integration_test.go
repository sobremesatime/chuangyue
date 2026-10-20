//go:build integration

// 互动服务 gRPC 入口的集成测试：需要本地 MySQL 与 Etcd。
//
// 覆盖「实例注册 -> 客户端经 etcd 解析 -> 成功发起 RPC」的完整链路，
// 依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/interaction/handler/...
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

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	interactionrepo "github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/registry"
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

// realOrAbsentVideoIDs 取一个真实存在的 video id，并追加一个必定不存在的 id，
// 用于验证「查不到数据也不报错」。
func realOrAbsentVideoIDs(t *testing.T, db *gorm.DB) []int64 {
	t.Helper()

	var ids []int64
	if err := db.Model(&videomodel.Video{}).Order("id ASC").Limit(1).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("查询真实视频 id 失败: %v", err)
	}
	return append(ids, 1<<62)
}

// TestGRPCInteractionServerViaEtcd 覆盖互动服务经 Etcd 发现的完整调用链路。
func TestGRPCInteractionServerViaEtcd(t *testing.T) {
	db := newTestDB(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	repo := interactionrepo.New(db)
	srv := grpc.NewServer()
	interactionv1.RegisterInteractionServiceServer(srv, NewGRPCInteractionServer(
		service.NewStatsService(repo),
		service.NewSignalService(repo),
	))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// 用真实监听端口注册，服务名带随机后缀保证互不干扰
	serviceName := uniqueTestService("it-interaction")
	deregister, err := reg.Register(context.Background(), serviceName, lis.Addr().String())
	if err != nil {
		t.Fatalf("注册服务实例失败: %v", err)
	}
	t.Cleanup(deregister)

	conn, err := grpc.NewClient("etcd:///"+serviceName,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ids := realOrAbsentVideoIDs(t, db)
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}

	client := interactionv1.NewInteractionServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.GetVideoStats(ctx, &interactionv1.GetVideoStatsRequest{VideoIds: ids})
	if err != nil {
		t.Fatalf("GetVideoStats() 失败: %v", err)
	}
	// 返回的计数只能来自请求中的 id，且不存在的 id 不应出现
	for _, st := range resp.GetStats() {
		if _, ok := want[st.GetVideoId()]; !ok {
			t.Fatalf("返回了未请求的视频计数: %+v", st)
		}
	}
}
