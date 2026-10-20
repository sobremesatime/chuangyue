package identity

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TestContextUser 验证身份的存取与未登录语义。
func TestContextUser(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("未写入时不应取到身份")
	}
	if got := UserID(context.Background()); got != 0 {
		t.Fatalf("未登录时 UserID 应为 0, 实际 %d", got)
	}
	if IsAdmin(context.Background()) {
		t.Fatal("未登录时不应判为管理员")
	}

	// ID <= 0 视为未登录，不写入。
	if _, ok := FromContext(WithUser(context.Background(), User{ID: 0})); ok {
		t.Fatal("ID 为 0 时不应写入身份")
	}

	ctx := WithUser(context.Background(), User{ID: 42, Role: RoleAdmin})
	u, ok := FromContext(ctx)
	if !ok || u.ID != 42 || u.Role != RoleAdmin {
		t.Fatalf("应取回写入的身份, 实际 %+v ok=%v", u, ok)
	}
	if !IsAdmin(ctx) || UserID(ctx) != 42 {
		t.Fatal("UserID / IsAdmin 应与写入的身份一致")
	}
}

// TestWithOutgoingUser 验证出站方向：有身份才写 metadata，且角色一并带上。
func TestWithOutgoingUser(t *testing.T) {
	t.Run("writes_when_present", func(t *testing.T) {
		ctx := withOutgoingUser(WithUser(context.Background(), User{ID: 7, Role: RoleAdmin}))

		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			t.Fatal("应写入出站 metadata")
		}
		if got := md.Get(MetadataUserID); len(got) != 1 || got[0] != "7" {
			t.Fatalf("user_id metadata 不符: %v", got)
		}
		if got := md.Get(MetadataRole); len(got) != 1 || got[0] != "1" {
			t.Fatalf("role metadata 不符: %v", got)
		}
	})

	t.Run("skips_when_absent", func(t *testing.T) {
		if _, ok := metadata.FromOutgoingContext(withOutgoingUser(context.Background())); ok {
			t.Fatal("未登录时不应写入出站 metadata")
		}
	})
}

// TestUnaryServerInterceptor 验证入站方向：metadata 里的身份会被交到业务 ctx。
func TestUnaryServerInterceptor(t *testing.T) {
	t.Run("admin", func(t *testing.T) {
		incoming := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			MetadataUserID, "9",
			MetadataRole, "1",
		))

		var id int64
		var admin bool
		_, err := UnaryServerInterceptor()(
			incoming, nil, &grpc.UnaryServerInfo{},
			func(ctx context.Context, _ any) (any, error) {
				id, admin = UserID(ctx), IsAdmin(ctx)
				return nil, nil
			},
		)
		if err != nil {
			t.Fatalf("拦截器不应返回错误: %v", err)
		}
		if id != 9 || !admin {
			t.Fatalf("业务 ctx 身份不符: id=%d admin=%v", id, admin)
		}
	})

	t.Run("missing_identity", func(t *testing.T) {
		var id int64
		_, err := UnaryServerInterceptor()(
			context.Background(), nil, &grpc.UnaryServerInfo{},
			func(ctx context.Context, _ any) (any, error) {
				id = UserID(ctx)
				return nil, nil
			},
		)
		if err != nil {
			t.Fatalf("拦截器不应返回错误: %v", err)
		}
		if id != 0 {
			t.Fatalf("没有身份时应为未登录, 实际 id=%d", id)
		}
	})

	t.Run("malformed_is_guest", func(t *testing.T) {
		// user_id 非数字、role 非 1：应收敛为「未登录」，而不是报错或误判管理员。
		incoming := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			MetadataUserID, "abc",
			MetadataRole, "7",
		))

		var id int64
		var admin bool
		_, err := UnaryServerInterceptor()(
			incoming, nil, &grpc.UnaryServerInfo{},
			func(ctx context.Context, _ any) (any, error) {
				id, admin = UserID(ctx), IsAdmin(ctx)
				return nil, nil
			},
		)
		if err != nil {
			t.Fatalf("拦截器不应返回错误: %v", err)
		}
		if id != 0 || admin {
			t.Fatalf("非法身份应收敛为未登录, 实际 id=%d admin=%v", id, admin)
		}
	})
}

// TestStreamServerInterceptor 验证流式方向：包装后的 ServerStream 暴露的 ctx 同样带身份。
func TestStreamServerInterceptor(t *testing.T) {
	incoming := metadata.NewIncomingContext(context.Background(), metadata.Pairs(MetadataUserID, "11"))
	stream := &fakeServerStream{ctx: incoming}

	var id int64
	err := StreamServerInterceptor()(nil, stream, &grpc.StreamServerInfo{},
		func(_ any, ss grpc.ServerStream) error {
			id = UserID(ss.Context())
			return nil
		},
	)
	if err != nil {
		t.Fatalf("拦截器不应返回错误: %v", err)
	}
	if id != 11 {
		t.Fatalf("流式业务 ctx 应拿到身份, 实际 id=%d", id)
	}
}

// fakeServerStream 只实现 ServerStream 中拦截器用得到的 Context 方法。
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }
