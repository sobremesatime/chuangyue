// Package middleware 提供通用的 Gin 中间件：链路追踪、异常恢复、访问日志、跨域、鉴权。
//
// 本包只依赖 pkg 内部的其他包，不引入任何业务概念。
package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/sobremesatime/chuangyue/pkg/response"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// 上下文键名，供 handler 通过 c.GetXxx 读取。
const (
	ContextUserID = "ctx_user_id"
	ContextRole   = "ctx_user_role"
)

// HeaderRequestID 是链路 ID 的请求 / 响应头名称。
const HeaderRequestID = "X-Request-ID"

// tracerName 是网关 HTTP 层的 instrumentation scope 名；
// 服务名由 tracing.Setup 写进 Resource，这里只是区分「是哪段代码产生的 span」。
const tracerName = "github.com/sobremesatime/chuangyue/gateway"

// TraceID 为每个请求建立链路上下文：既生成日志用的 trace_id，也起一个 HTTP 服务端 span。
//
// 两件事合并在这里做，是为了让**日志里的 trace_id 与 Jaeger 里的 trace 是同一个值**：
// trace_id 直接取自 span 的 TraceID，排障时能两头互跳。
//
// 若上游（如 Nginx 或已接入追踪的调用方）带 W3C `traceparent`，则沿用其链路、不新建；
// 否则新建一条根 trace。trace_id 同时写入上下文与响应头 X-Request-ID。
func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		// provider / 传播器都从全局现取：tracing.Setup 可能晚于中间件构造，
		// 构造时捕获会拿到 no-op 实例（这样追踪就静默失效了）。
		tracer := otel.Tracer(tracerName)
		prop := otel.GetTextMapPropagator()

		ctx := prop.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		// span 名用「方法 + 路由模板」，路由模板已由 gin 在进入中间件前解析完成。
		name := c.FullPath()
		if name == "" {
			name = c.Request.Method + " " + c.Request.URL.Path
		} else {
			name = c.Request.Method + " " + name
		}
		ctx, span := tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		// 未启用追踪（全局 no-op provider）时 SpanContext 无效，退回「上游头 -> 自生成」的旧路径，
		// 保证日志链路 ID 依然可用。
		traceID := span.SpanContext().TraceID().String()
		if !span.SpanContext().IsValid() {
			traceID = c.GetHeader(HeaderRequestID)
			if traceID == "" {
				traceID = fmt.Sprintf("%x", snowflake.NextID())
			}
		}

		c.Set(response.TraceIDKey, traceID)
		c.Writer.Header().Set(HeaderRequestID, traceID)
		// 同时写进请求 context：下游 gRPC 调用的客户端拦截器会把它带进 metadata，
		// 于是内部服务的日志能与本次请求串在同一个 trace_id 下；
		// 而 otelgrpc 的统计钩子会把这个 context 里的 span 经 traceparent 传给下游。
		c.Request = c.Request.WithContext(tracing.WithTraceID(ctx, traceID))

		c.Next()

		// 把结果写回 span，便于在 Jaeger 里按状态码 / 路由过滤与定位慢请求。
		status := c.Writer.Status()
		span.SetAttributes(
			attribute.String("http.request.method", c.Request.Method),
			attribute.String("url.path", c.Request.URL.Path),
			attribute.Int("http.response.status_code", status),
		)
		if route := c.FullPath(); route != "" {
			span.SetAttributes(attribute.String("http.route", route))
		}
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	}
}
