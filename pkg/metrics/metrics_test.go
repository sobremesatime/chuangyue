package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// scrape 触发一次 /metrics 抓取并返回响应正文。
func scrape(t *testing.T) string {
	t.Helper()

	rec := httptest.NewRecorder()
	Default().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics 应返回 200，实际 %d", rec.Code)
	}
	return rec.Body.String()
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("/metrics 中未找到 %q", want)
	}
}

// HTTP 中间件应按「路由模板」而非真实路径打标签（避免时间序列爆炸）。
func TestGinMiddleware_UsesRouteTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Default().GinMiddleware())
	r.GET("/api/v1/videos/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/videos/2109676650810576896", nil))

	body := scrape(t)
	mustContain(t, body, `chuangyue_http_requests_total{method="GET",path="/api/v1/videos/:id",status="200"} 1`)
	if strings.Contains(body, "2109676650810576896") {
		t.Fatal("不应把真实路径（含 ID）作为标签，否则时间序列会爆炸")
	}
}

// 未命中路由的请求统一记为 unmatched。
func TestGinMiddleware_UnmatchedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Default().GinMiddleware())

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no/such/route", nil))

	mustContain(t, scrape(t), `chuangyue_http_requests_total{method="GET",path="unmatched",status="404"} 1`)
}

// 出站 gRPC 调用按「目标服务 + 方法 + 状态码」统计。
func TestUnaryClientInterceptor(t *testing.T) {
	it := Default().UnaryClientInterceptor("video")
	invoker := func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		return status.Error(codes.DeadlineExceeded, "timeout")
	}

	if err := it(context.Background(), "/video.v1.VideoService/GetVideo", nil, nil, nil, invoker); err == nil {
		t.Fatal("期望返回错误")
	}

	body := scrape(t)
	mustContain(t, body, `chuangyue_grpc_client_requests_total{code="DeadlineExceeded",method="GetVideo",target="video"} 1`)
}

// 服务端一元拦截器按「服务 + 方法 + 状态码」统计。
func TestUnaryServerInterceptor(t *testing.T) {
	it := Default().UnaryServerInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/interaction.v1.CommentService/ListComments"}
	handler := func(context.Context, any) (any, error) { return nil, nil }

	if _, err := it(context.Background(), nil, info, handler); err != nil {
		t.Fatalf("handler 不应报错: %v", err)
	}

	body := scrape(t)
	mustContain(t, body, `chuangyue_grpc_server_requests_total{code="OK",method="ListComments",service="interaction.v1.CommentService"} 1`)
}

// 限流拒绝数与熔断器状态都要能被抓到。
func TestRateLimitAndBreakerMetrics(t *testing.T) {
	Default().IncRateLimited("ip")
	Default().RegisterBreakerState("video", func() float64 { return 2 })

	body := scrape(t)
	mustContain(t, body, `chuangyue_ratelimit_rejected_total{dimension="ip"} 1`)
	mustContain(t, body, `chuangyue_breaker_state{service="video"} 2`)
}

// 后台任务指标：转码最终结果、周期任务的间隔 / 执行次数 / 最近成功时间。
func TestBackgroundJobMetrics(t *testing.T) {
	m := Default()
	m.ObserveTranscodeResult("failed")
	m.RegisterCronJob("it-cron", 30*time.Second)
	m.ObserveCronRun("it-cron", "success")
	m.ObserveCronRun("it-cron", "skipped")

	body := scrape(t)
	mustContain(t, body, `chuangyue_transcode_jobs_total{result="failed"}`)
	mustContain(t, body, `chuangyue_cron_job_interval_seconds{job="it-cron"} 30`)
	mustContain(t, body, `chuangyue_cron_job_runs_total{job="it-cron",result="success"}`)
	mustContain(t, body, `chuangyue_cron_job_runs_total{job="it-cron",result="skipped"}`)
	mustContain(t, body, `chuangyue_cron_job_last_success_timestamp_seconds{job="it-cron"}`)
}

// 运行时指标应一并暴露（依赖默认注册表）。
func TestRuntimeMetricsPresent(t *testing.T) {
	mustContain(t, scrape(t), "go_goroutines")
}

// pprof 端点必须能从自建 mux 访问到——net/http/pprof 只挂 DefaultServeMux，
// 若漏了显式登记，/debug/pprof 会全部 404。
func TestPprofHandler(t *testing.T) {
	h := PprofHandler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/debug/pprof/ 应返回 200，实际 %d", rec.Code)
	}
	// 首页会列出各命名 profile 的链接，含 goroutine。
	if !strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatal("pprof 首页应列出命名 profile（如 goroutine）")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/cmdline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/debug/pprof/cmdline 应返回 200，实际 %d", rec.Code)
	}
}
