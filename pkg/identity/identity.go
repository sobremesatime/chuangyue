// Package identity 在服务之间透传「已认证的调用者身份」。
//
// 网关完成 JWT 校验后把身份写进请求 context，客户端拦截器把它放进 gRPC metadata，
// 下游的服务端拦截器再取回 context——于是业务代码从 ctx 读身份，而不必在每条请求消息里
// 重复携带 user_id / is_admin 字段（那些字段本质不是业务数据，散在契约里既啰嗦又容易漏带）。
//
// 安全边界（重要）：内部 gRPC 是内网明文，任何能连上的进程都能伪造 metadata——这与直接
// 伪造请求字段的难度一样。本包解决的是「身份载体统一」，不提供额外的防伪能力。
package identity

import (
	"context"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// metadata 键名。HTTP/2 头名统一小写，便于跨语言互通。
const (
	MetadataUserID = "x-user-id"
	MetadataRole   = "x-user-role"
)

// 角色常量，与 internal/user/model 的 RBAC 语义保持一致（同一套权限模型）。
const (
	RoleUser  int8 = 0
	RoleAdmin int8 = 1
)

// User 是调用者身份；ID <= 0 表示未登录。
type User struct {
	ID   int64
	Role int8
}

// ctxKey 是本包私有的 context 键类型，避免与其它包的键冲突。
type ctxKey struct{}

// WithUser 把身份放进 context；未登录（ID <= 0）时原样返回，不写入。
func WithUser(ctx context.Context, u User) context.Context {
	if u.ID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, u)
}

// FromContext 取出身份；未登录或未经透传时 ok 为 false。
func FromContext(ctx context.Context) (User, bool) {
	if ctx == nil {
		return User{}, false
	}
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// UserID 取调用者 id，未登录返回 0。
func UserID(ctx context.Context) int64 {
	u, _ := FromContext(ctx)
	return u.ID
}

// IsAdmin 判断调用者是否为管理员。
func IsAdmin(ctx context.Context) bool {
	u, _ := FromContext(ctx)
	return u.Role == RoleAdmin
}

// UnaryServerInterceptor 把入站 metadata 里的身份写进 context。
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(withIncomingUser(ctx), req)
	}
}

// StreamServerInterceptor 是流式版本（上传 / 头像这类长连接同样需要身份）。
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &userServerStream{ServerStream: ss, ctx: withIncomingUser(ss.Context())})
	}
}

// UnaryClientInterceptor 把 context 里的身份追加到出站 metadata；未登录时不写。
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(withOutgoingUser(ctx), method, req, reply, cc, opts...)
	}
}

// StreamClientInterceptor 是流式版本。
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(withOutgoingUser(ctx), desc, cc, method, opts...)
	}
}

// withIncomingUser 从入站 metadata 取身份放进 context；解析不出身份时原样返回。
func withIncomingUser(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}

	id := firstInt64(md.Get(MetadataUserID))
	if id <= 0 {
		return ctx
	}

	u := User{ID: id}
	if firstInt64(md.Get(MetadataRole)) == int64(RoleAdmin) {
		u.Role = RoleAdmin
	}
	return WithUser(ctx, u)
}

// withOutgoingUser 把 context 里的身份追加到出站 metadata；未登录时原样返回。
func withOutgoingUser(ctx context.Context) context.Context {
	u, ok := FromContext(ctx)
	if !ok {
		return ctx
	}
	return metadata.AppendToOutgoingContext(
		ctx,
		MetadataUserID, strconv.FormatInt(u.ID, 10),
		MetadataRole, strconv.FormatInt(int64(u.Role), 10),
	)
}

// firstInt64 取 metadata 值的第一个并转成 int64；缺失或非法都返回 0。
func firstInt64(vals []string) int64 {
	if len(vals) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(vals[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// userServerStream 用替换过 context 的 ServerStream 包装原流，
// 使拦截器写进 ctx 的身份对流内方法也可见。
type userServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *userServerStream) Context() context.Context { return s.ctx }
