// Package registry 基于 Etcd 提供服务注册与发现。
//
// 微服务启动时把「服务名 + 实例地址」写入 Etcd（带租约），调用方按服务名
// 拉取当前健康实例；进程退出或租约到期后实例自动摘除。
// 本包不感知 gRPC 细节，gRPC 侧的解析器在 resolver.go。
package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/logger"
)

const (
	// keyPrefix 是注册中心所有键的统一前缀，形状为 /chuangyue/services/<service>/<addr>。
	// 用固定前缀把注册数据与同一 Etcd 集群里的其它键隔开，便于按前缀整段发现与清理。
	keyPrefix = "/chuangyue/services"

	// leaseTTL 是实例租约时长（秒）。取值偏短，让未续租的实例尽快下线；
	// keepalive 会按 TTL/3 的频率自动续期，正常运行时不会因到点被摘除。
	leaseTTL = 10

	// dialTimeout 用于连接 Etcd、执行单次读写与注销，避免网络异常时无限阻塞。
	dialTimeout = 3 * time.Second
)

// Registry 是 Etcd 服务注册中心的客户端封装。
type Registry struct {
	cli *clientv3.Client
}

// New 连接 Etcd 并返回注册中心客户端；endpoints 为空时使用 127.0.0.1:2379。
//
// clientv3.New 本身是惰性建连，这里额外做一次探活，
// 让「连不上」在启动阶段就暴露，而不是等到首次注册/发现才报错。
func New(endpoints []string) (*Registry, error) {
	if len(endpoints) == 0 {
		endpoints = []string{"127.0.0.1:2379"}
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: dialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("连接 Etcd 失败: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	if _, err := cli.Get(ctx, keyPrefix); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("连接 Etcd 失败: %w", err)
	}

	logger.L().Info("Etcd 连接成功", zap.Strings("endpoints", endpoints))
	return &Registry{cli: cli}, nil
}

// Register 把服务实例注册到 Etcd（带租约与自动续期），返回幂等的注销函数。
//
// 注册键为 /chuangyue/services/<service>/<addr>，值为 addr；
// 传入的 ctx 控制续租生命周期，ctx 取消后租约到期，实例会自动下线。
// 返回的注销函数重复调用不报错。
func (r *Registry) Register(ctx context.Context, service, addr string) (func(), error) {
	if service == "" || addr == "" {
		return nil, fmt.Errorf("注册服务实例失败: service 与 addr 不能为空")
	}
	key := instanceKey(service, addr)

	lease, err := r.cli.Grant(ctx, leaseTTL)
	if err != nil {
		return nil, fmt.Errorf("创建 Etcd 租约失败: %w", err)
	}
	if _, err := r.cli.Put(ctx, key, addr, clientv3.WithLease(lease.ID)); err != nil {
		// 写入失败时及时撤租，避免留下空租约
		_, _ = r.cli.Revoke(context.Background(), lease.ID)
		return nil, fmt.Errorf("写入服务实例失败: %w", err)
	}

	// 续租协程与传入 ctx 绑定：ctx 取消即停止续租，租约到期后键被自动删除。
	keepCtx, stopKeepAlive := context.WithCancel(ctx)
	ch, err := r.cli.KeepAlive(keepCtx, lease.ID)
	if err != nil {
		stopKeepAlive()
		_, _ = r.cli.Revoke(context.Background(), lease.ID)
		return nil, fmt.Errorf("启动租约续期失败: %w", err)
	}
	// 必须持续消费续租响应通道，否则 KeepAlive 内部会停止续期。
	go func() {
		for range ch {
		}
	}()

	var once sync.Once
	deregister := func() {
		once.Do(func() {
			stopKeepAlive()
			// 注销单独用短超时 ctx：调用方的 ctx 可能已取消，不能复用它。
			ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			defer cancel()
			if _, err := r.cli.Revoke(ctx, lease.ID); err != nil {
				logger.L().Warn("撤销服务实例租约失败",
					zap.String("service", service), zap.String("addr", addr), zap.Error(err))
			}
		})
	}

	logger.L().Info("服务实例已注册", zap.String("service", service), zap.String("addr", addr))
	return deregister, nil
}

// Discover 返回某服务当前所有健康实例地址（形如 127.0.0.1:9101）。
func (r *Registry) Discover(ctx context.Context, service string) ([]string, error) {
	if service == "" {
		return nil, fmt.Errorf("发现服务实例失败: service 不能为空")
	}

	resp, err := r.cli.Get(ctx, servicePrefix(service), clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("查询服务实例失败: %w", err)
	}

	addrs := make([]string, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		addrs = append(addrs, string(kv.Value))
	}
	return addrs, nil
}

// Close 关闭底层 Etcd 连接。
func (r *Registry) Close() error {
	if r == nil || r.cli == nil {
		return nil
	}
	return r.cli.Close()
}

// servicePrefix 返回某服务下所有实例键的前缀。
func servicePrefix(service string) string {
	return keyPrefix + "/" + service + "/"
}

// instanceKey 返回单个实例的注册键。
func instanceKey(service, addr string) string {
	return servicePrefix(service) + addr
}
