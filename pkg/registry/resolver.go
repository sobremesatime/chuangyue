package registry

import (
	"context"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc/resolver"

	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// schemeEtcd 是 gRPC 解析器注册的 scheme，客户端用 etcd:///<service> 拨号。
const schemeEtcd = "etcd"

// RegisterResolver 注册 scheme 为 "etcd" 的 gRPC 解析器，
// 使客户端可以用 etcd:///<service> 形式拨号（例如 etcd:///interaction）。
func RegisterResolver(r *Registry) {
	resolver.Register(&etcdBuilder{reg: r})
}

// etcdBuilder 按 target 里解析出的服务名构建解析器。
type etcdBuilder struct {
	reg *Registry
}

// Scheme 返回本解析器支持的 scheme。
func (b *etcdBuilder) Scheme() string {
	return schemeEtcd
}

// Build 从 target 解析服务名并启动实例监听。
//
// 服务名取自 target.URL.Path（形如 etcd:///interaction 的 "/interaction"），
// 不用已废弃的 target.Endpoint()。
func (b *etcdBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	service := strings.TrimPrefix(target.URL.Path, "/")
	if service == "" {
		return nil, fmt.Errorf("构建 Etcd 解析器失败: 目标缺少服务名，形如 etcd:///<service>")
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &etcdResolver{
		reg:     b.reg,
		service: service,
		cc:      cc,
		ctx:     ctx,
		cancel:  cancel,
	}
	go r.watch()
	return r, nil
}

// etcdResolver 监听某个服务的实例变化并推送给 gRPC。
type etcdResolver struct {
	reg     *Registry
	service string
	cc      resolver.ClientConn
	ctx     context.Context
	cancel  context.CancelFunc
}

// ResolveNow 是 gRPC 的重新解析提示。本解析器基于 Watch 实时推送，
// 无需在提示时主动重查，保留空实现以满足接口。
func (e *etcdResolver) ResolveNow(resolver.ResolveNowOptions) {}

// Close 停止监听并释放资源。
func (e *etcdResolver) Close() {
	e.cancel()
}

// watch 先做一次全量查询给出初始实例，再按 revision 增量监听前缀变化。
func (e *etcdResolver) watch() {
	prefix := servicePrefix(e.service)

	ctx, cancel := context.WithTimeout(e.ctx, dialTimeout)
	resp, err := e.reg.cli.Get(ctx, prefix, clientv3.WithPrefix())
	cancel()

	// 记录初始 revision：从 rev+1 开始监听，避免查询与监听之间的变化被漏掉。
	var rev int64
	if err != nil {
		logger.L().Warn("解析服务实例失败", zap.String("service", e.service), zap.Error(err))
		e.cc.ReportError(err)
	} else {
		rev = resp.Header.Revision
		addrs := make([]string, 0, len(resp.Kvs))
		for _, kv := range resp.Kvs {
			addrs = append(addrs, string(kv.Value))
		}
		e.publish(addrs)
	}

	opts := []clientv3.OpOption{clientv3.WithPrefix()}
	if rev > 0 {
		opts = append(opts, clientv3.WithRev(rev+1))
	}

	for wresp := range e.reg.cli.Watch(e.ctx, prefix, opts...) {
		if wresp.Canceled {
			logger.L().Warn("服务实例监听中断", zap.String("service", e.service), zap.Error(wresp.Err()))
			return
		}
		// 实例增删只需重新拉一次全量，实现简单且天然去重。
		addrs, err := e.reg.Discover(e.ctx, e.service)
		if err != nil {
			logger.L().Warn("刷新服务实例失败", zap.String("service", e.service), zap.Error(err))
			continue
		}
		e.publish(addrs)
	}
}

// publish 把最新实例列表推送给 gRPC。
func (e *etcdResolver) publish(addrs []string) {
	state := resolver.State{Endpoints: make([]resolver.Endpoint, 0, len(addrs))}
	for _, addr := range addrs {
		state.Endpoints = append(state.Endpoints, resolver.Endpoint{
			Addresses: []resolver.Address{{Addr: addr}},
		})
	}
	if err := e.cc.UpdateState(state); err != nil {
		logger.L().Warn("更新服务实例状态失败", zap.String("service", e.service), zap.Error(err))
		return
	}
	logger.L().Info("服务实例已更新", zap.String("service", e.service), zap.Strings("addrs", addrs))
}
