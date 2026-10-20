//go:build integration

// 视频服务 gRPC 入口的集成测试：需要本地 MySQL 与 Etcd。
//
// 覆盖「实例注册 -> 客户端经 etcd 解析 -> 成功发起 RPC」的完整链路，
// 依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/video/handler/...
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/internal/video/service"
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

// firstVideoID 取一个真实存在的 video id，取不到返回 0。
func firstVideoID(t *testing.T, db *gorm.DB) int64 {
	t.Helper()

	var ids []int64
	if err := db.Model(&videomodel.Video{}).Order("id ASC").Limit(1).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("查询真实视频 id 失败: %v", err)
	}
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

// TestGRPCVideoServerViaEtcd 覆盖视频服务经 Etcd 发现的完整调用链路。
func TestGRPCVideoServerViaEtcd(t *testing.T) {
	db := newTestDB(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	srv := grpc.NewServer()
	videov1.RegisterVideoServiceServer(srv, NewGRPCVideoServer(service.New(videorepo.New(db), nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, nil)))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// 用真实监听端口注册，服务名带随机后缀保证互不干扰
	serviceName := uniqueTestService("it-video")
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

	client := videov1.NewVideoServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	realID := firstVideoID(t, db)
	absentID := int64(1 << 62)

	// GetVideos：真实 id 与不存在的 id 一起批量查，不应报错
	ids := []int64{absentID}
	if realID > 0 {
		ids = append(ids, realID)
	}
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}

	resp, err := client.GetVideos(ctx, &videov1.GetVideosRequest{Ids: ids})
	if err != nil {
		t.Fatalf("GetVideos() 失败: %v", err)
	}
	for _, v := range resp.GetVideos() {
		if _, ok := want[v.GetId()]; !ok {
			t.Fatalf("返回了未请求的视频: %+v", v)
		}
	}

	// GetVideo：存在则返回，不存在必须是 NotFound
	if realID > 0 {
		one, err := client.GetVideo(ctx, &videov1.GetVideoRequest{Id: realID})
		if err != nil {
			t.Fatalf("GetVideo(%d) 失败: %v", realID, err)
		}
		if one.GetVideo().GetId() != realID {
			t.Fatalf("GetVideo(%d) 返回 id=%d", realID, one.GetVideo().GetId())
		}
	}

	if _, err := client.GetVideo(ctx, &videov1.GetVideoRequest{Id: absentID}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetVideo(不存在) 应返回 NotFound, 实际: %v", err)
	}
}
