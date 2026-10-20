// Package tracing 在服务之间透传链路 ID（trace_id）。
//
// 网关在每个请求上生成（或沿用上游传来的）trace_id，经 gRPC metadata 传给下游服务；
// 下游的服务端拦截器把它取回 context，于是同一次请求在各服务里的日志可以用同一个
// trace_id 串起来——这也是后续接 Jaeger 展示完整链路的基础。
//
// 本包只关心「透传」，不依赖日志库：日志侧的取用见 pkg/logger.TraceField。
package tracing

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// MetadataKey 是 trace_id 在 gRPC metadata 中的键名。
// HTTP/2 头名统一小写，这里也用全小写以保证跨语言互通。
const MetadataKey = "x-trace-id"

// ctxKey 是本包私有的 context 键类型，避免与其它包的键冲突。
type ctxKey struct{}

// WithTraceID 把 trace_id 放进 context；空串视为「没有」直接返回原 ctx。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, traceID)
}

// TraceID 从 context 取出 trace_id，没有则返回空串。
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// UnaryServerInterceptor 把入站 metadata 里的 trace_id 写进 context。
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(withIncomingTraceID(ctx), req)
	}
}

// StreamServerInterceptor 是流式版本：上传 / 头像这类长连接同样需要串联。
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &traceServerStream{ServerStream: ss, ctx: withIncomingTraceID(ss.Context())})
	}
}

// UnaryClientInterceptor 把 context 里的 trace_id 追加到出站 metadata。
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(withOutgoingTraceID(ctx), method, req, reply, cc, opts...)
	}
}

// StreamClientInterceptor 是流式版本。
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(withOutgoingTraceID(ctx), desc, cc, method, opts...)
	}
}

// withIncomingTraceID 从入站 metadata 取 trace_id 放进 context。
func withIncomingTraceID(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	if vals := md.Get(MetadataKey); len(vals) > 0 {
		return WithTraceID(ctx, vals[0])
	}
	return ctx
}

// withOutgoingTraceID 把 context 里的 trace_id 追加到出站 metadata；没有则原样返回。
func withOutgoingTraceID(ctx context.Context) context.Context {
	id := TraceID(ctx)
	if id == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, MetadataKey, id)
}

// traceServerStream 用替换过 context 的 ServerStream 包装原流，
// 使拦截器写进 ctx 的 trace_id 对流内的方法（如 Recv / Send 后的业务处理）也可见。
type traceServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *traceServerStream) Context() context.Context { return s.ctx }
