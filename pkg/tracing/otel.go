package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup 初始化 OpenTelemetry 链路追踪，把 span 经 OTLP 上报到 Jaeger。
//
// 与「自研 trace_id 透传」（本文件之外的部分）的关系：后者负责把同一个链路 ID 串进**日志**，
// 前者负责把**完整 span 树**送到 Jaeger。两者通过同一个 TraceID 对齐——网关的
// TraceID 中间件改用本次 span 的 TraceID 作为日志 ID，于是日志里的 trace_id 与 Jaeger 里的
// trace 是同一个值，排障时能两边互跳。
//
// 参数：
//   - serviceName：写入 Resource 的服务名，决定 Jaeger 里按服务筛选的口径；
//   - endpoint：OTLP gRPC 接收地址（如 "jaeger:4317"）。**留空表示禁用追踪**——此时保持全局
//     no-op provider，不产生任何 span，也不影响其余功能（日志链路 ID 照旧工作）；
//   - sampleRatio：根 span 采样率（0~1）。采样用 ParentBased 包住 TraceIDRatioBased：
//     根 span 按比例抽，子 span 一律沿用父决策——保证「要么整条链路都在、要么都不在」，
//     不会出现半截链路（断链的追踪比没有追踪更误导人）。
//
// 需在构造 gRPC server / 发起调用之前调用一次；随进程 ctx 取消自动 flush 并关闭。
// 连接失败只返回错误、**不应阻断启动**：追踪是旁路，不该成为服务能不能起来的前提。
func Setup(ctx context.Context, serviceName, endpoint string, sampleRatio float64) error {
	if endpoint == "" {
		return nil
	}
	sampleRatio = clamp01(sampleRatio)

	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return fmt.Errorf("初始化 OTLP exporter 失败: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		// 批处理上报：span 先在内存攒批、异步发送，避免每个 span 都走一次网络。
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio))),
	)
	otel.SetTracerProvider(tp)
	// W3C TraceContext（traceparent 头）是跨进程传播的事实标准，各语言 SDK 与 Jaeger 都认；
	// Baggage 用于透传业务键值（当前未使用，一并装上以便后续按需扩展）。
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// 与 metrics 服务同一模式：随进程 ctx 取消 flush 缓冲中的 span（最长等 5s）。
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutdownCtx)
	}()
	return nil
}

// clamp01 把采样率夹到 [0,1]，避免配置写超范围导致采样行为不可预期。
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
