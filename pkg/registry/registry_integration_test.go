//go:build integration

// 服务注册中心的集成测试：需要本地 Etcd。
//
// 默认不参与 `go test ./...`（用 integration 构建标签隔离），
// 需要真实注册中心时显式执行：
//
//	docker compose -f deploy/compose/docker-compose.yml --profile full up -d etcd
//	go test -tags integration -v -run TestRegistry ./pkg/registry/...
package registry

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
)

// newTestRegistry 连到本地 Etcd；连不上直接跳过，避免无依赖环境误报失败。
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()

	reg, err := New([]string{"127.0.0.1:2379"})
	if err != nil {
		t.Skipf("Etcd 不可用，跳过集成测试（请先启动容器：docker compose -f deploy/compose/docker-compose.yml --profile full up -d etcd）: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

// uniqueService 生成唯一服务名，避免历史残留实例互相干扰。
func uniqueService(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// waitForAddr 轮询等待目标实例出现。
func waitForAddr(t *testing.T, reg *Registry, service, want string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		addrs, err := reg.Discover(context.Background(), service)
		if err == nil {
			for _, a := range addrs {
				if a == want {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("超时前未发现实例 %s", want)
}

// waitForGone 轮询等待目标实例消失。
func waitForGone(t *testing.T, reg *Registry, service, gone string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		addrs, err := reg.Discover(context.Background(), service)
		if err == nil {
			found := false
			for _, a := range addrs {
				if a == gone {
					found = true
					break
				}
			}
			if !found {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("注销后实例 %s 仍可被发现", gone)
}

// TestRegistryRegisterDiscover 覆盖注册 → 发现 → 注销的完整链路。
func TestRegistryRegisterDiscover(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	service := uniqueService("it-discover")
	addr := "127.0.0.1:9199"

	deregister, err := reg.Register(ctx, service, addr)
	if err != nil {
		t.Fatalf("Register() 失败: %v", err)
	}
	// 重复调用注销函数必须幂等，这里顺带验证不会 panic / 报错
	t.Cleanup(deregister)
	t.Cleanup(deregister)

	waitForAddr(t, reg, service, addr)

	deregister()
	waitForGone(t, reg, service, addr)
}

// stubInteractionServer 是返回固定值的最小桩，用于验证解析器链路。
type stubInteractionServer struct {
	interactionv1.UnimplementedInteractionServiceServer
}

func (stubInteractionServer) GetVideoStats(_ context.Context, in *interactionv1.GetVideoStatsRequest) (*interactionv1.GetVideoStatsResponse, error) {
	stats := make([]*interactionv1.VideoStats, 0, len(in.GetVideoIds()))
	for _, id := range in.GetVideoIds() {
		stats = append(stats, &interactionv1.VideoStats{VideoId: id, LikeCount: 1})
	}
	return &interactionv1.GetVideoStatsResponse{Stats: stats}, nil
}

// TestRegistryResolver 覆盖 gRPC 解析器链路：注册实例后用 etcd:/// 拨号并发起一次 RPC。
func TestRegistryResolver(t *testing.T) {
	reg := newTestRegistry(t)
	RegisterResolver(reg)

	// 起一个监听随机端口的本地 gRPC 服务
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	srv := grpc.NewServer()
	interactionv1.RegisterInteractionServiceServer(srv, stubInteractionServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	service := uniqueService("it-resolver")
	addr := lis.Addr().String()

	deregister, err := reg.Register(context.Background(), service, addr)
	if err != nil {
		t.Fatalf("Register() 失败: %v", err)
	}
	t.Cleanup(deregister)

	conn, err := grpc.NewClient("etcd:///"+service,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(resolver.Get(schemeEtcd)),
	)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := interactionv1.NewInteractionServiceClient(conn)
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.GetVideoStats(callCtx, &interactionv1.GetVideoStatsRequest{VideoIds: []int64{7}})
	if err != nil {
		t.Fatalf("GetVideoStats() 失败: %v", err)
	}
	if len(resp.GetStats()) != 1 || resp.GetStats()[0].GetVideoId() != 7 {
		t.Fatalf("返回结果不符合预期: %+v", resp.GetStats())
	}
}
