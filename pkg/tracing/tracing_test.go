package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TestTraceIDContext 验证 trace_id 的存取：写进去能取出来，空串视为「没有」。
func TestTraceIDContext(t *testing.T) {
	if got := TraceID(context.Background()); got != "" {
		t.Fatalf("未写入时应返回空串, 实际 %q", got)
	}

	ctx := WithTraceID(context.Background(), "trace-1")
	if got := TraceID(ctx); got != "trace-1" {
		t.Fatalf("应取回写入的 trace_id, 实际 %q", got)
	}

	// 空串不应覆盖已有值，也不应写进 context。
	if got := TraceID(WithTraceID(ctx, "")); got != "trace-1" {
		t.Fatalf("空串不应影响既有 trace_id, 实际 %q", got)
	}
}

// TestWithOutgoingTraceID 验证出站方向：有 trace_id 才写 metadata。
func TestWithOutgoingTraceID(t *testing.T) {
	t.Run("writes_when_present", func(t *testing.T) {
		ctx := withOutgoingTraceID(WithTraceID(context.Background(), "trace-2"))

		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			t.Fatal("应写入出站 metadata")
		}
		if got := md.Get(MetadataKey); len(got) != 1 || got[0] != "trace-2" {
			t.Fatalf("出站 metadata 中的 trace_id 不符: %v", got)
		}
	})

	t.Run("skips_when_absent", func(t *testing.T) {
		if _, ok := metadata.FromOutgoingContext(withOutgoingTraceID(context.Background())); ok {
			t.Fatal("没有 trace_id 时不应写入出站 metadata")
		}
	})
}

// TestUnaryServerInterceptor 验证入站方向：metadata 里的 trace_id 会被交到业务 ctx。
func TestUnaryServerInterceptor(t *testing.T) {
	incoming := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs(MetadataKey, "trace-3"),
	)

	var got string
	_, err := UnaryServerInterceptor()(
		incoming, nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			got = TraceID(ctx)
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("拦截器不应返回错误: %v", err)
	}
	if got != "trace-3" {
		t.Fatalf("业务 ctx 应拿到 metadata 里的 trace_id, 实际 %q", got)
	}
}

// TestStreamServerInterceptor 验证流式方向：包装后的 ServerStream 暴露的 ctx 同样带 trace_id。
func TestStreamServerInterceptor(t *testing.T) {
	incoming := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs(MetadataKey, "trace-4"),
	)
	stream := &fakeServerStream{ctx: incoming}

	var got string
	err := StreamServerInterceptor()(nil, stream, &grpc.StreamServerInfo{},
		func(_ any, ss grpc.ServerStream) error {
			got = TraceID(ss.Context())
			return nil
		},
	)
	if err != nil {
		t.Fatalf("拦截器不应返回错误: %v", err)
	}
	if got != "trace-4" {
		t.Fatalf("流式业务 ctx 应拿到 trace_id, 实际 %q", got)
	}
}

// fakeServerStream 只实现 ServerStream 中拦截器用得到的 Context 方法。
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

// TestSetupDisabled 验证未配置 endpoint 时追踪保持关闭：不报错，也不替换全局 provider。
func TestSetupDisabled(t *testing.T) {
	before := otel.GetTracerProvider()
	if err := Setup(context.Background(), "test-svc", "", 1); err != nil {
		t.Fatalf("未配置 endpoint 时 Setup 不应报错: %v", err)
	}
	if otel.GetTracerProvider() != before {
		t.Fatal("禁用追踪时不应替换全局 TracerProvider")
	}
}

// TestClamp01 采样率会被夹到 [0,1]，避免配置越界导致采样行为不可预期。
func TestClamp01(t *testing.T) {
	cases := map[float64]float64{-1: 0, 0: 0, 0.5: 0.5, 1: 1, 2: 1}
	for in, want := range cases {
		if got := clamp01(in); got != want {
			t.Fatalf("clamp01(%v) = %v, 期望 %v", in, got, want)
		}
	}
}
