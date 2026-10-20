//go:build integration

// 网关视频读路径的集成测试：起一个真实 video gRPC server（真实 MySQL，注册到 Etcd），
// 用 etcd:/// 解析出的客户端 + httptest 起的 gin 路由走完整链路，
// 断言 HTTP 响应 JSON 与从数据库直查得到的结果一致。
//
// 依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/gateway/handler/...
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	videohandler "github.com/sobremesatime/chuangyue/internal/video/handler"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	videosvc "github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/registry"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// newTestDB 自包含地连本地 MySQL（DSN 各段可用环境变量覆盖），连不上直接跳过。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	cfg := config.MySQLConfig{
		Host:         envOr("MYSQL_HOST", "127.0.0.1"),
		Port:         envIntOr("MYSQL_PORT", 3306),
		Username:     envOr("MYSQL_USER", "root"),
		Password:     envOr("MYSQL_PASSWORD", "root123456"),
		Database:     envOr("MYSQL_DATABASE", "chuangyue"),
		Charset:      "utf8mb4",
		MaxIdleConns: 2,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	}
	db, err := database.NewMySQL(cfg)
	if err != nil {
		t.Skipf("MySQL 不可用，跳过集成测试: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

// newTestRegistry 连本地 Etcd，连不上直接跳过。
func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()

	reg, err := registry.New([]string{envOr("ETCD_ENDPOINTS", "127.0.0.1:2379")})
	if err != nil {
		t.Skipf("Etcd 不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

// listEnvelope 对应列表接口的响应信封，data 为分页结构。
type listEnvelope struct {
	Code int `json:"code"`
	Data struct {
		List     []dto.VideoInfo `json:"list"`
		Total    int64           `json:"total"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
	} `json:"data"`
}

// detailEnvelope 对应详情接口的响应信封。
type detailEnvelope struct {
	Code int           `json:"code"`
	Data dto.VideoInfo `json:"data"`
}

// codeEnvelope 只取业务错误码，用于断言错误语义。
type codeEnvelope struct {
	Code int `json:"code"`
}

// expectedInfo 独立地把数据库行映射为对外 DTO，作为与 HTTP 响应比对的基准。
func expectedInfo(v videomodel.Video) dto.VideoInfo {
	return dto.VideoInfo{
		ID:            v.ID,
		Title:         v.Title,
		Description:   v.Description,
		CoverURL:      v.CoverURL,
		PlayURL:       v.PlayURL,
		Duration:      v.Duration,
		CategoryID:    v.CategoryID,
		ViewCount:     v.ViewCount,
		LikeCount:     v.LikeCount,
		FavoriteCount: v.FavoriteCount,
		Status:        v.Status,
		ErrorMsg:      v.ErrorMsg,
		CreatedAt:     v.CreatedAt,
	}
}

// normTime 把时间统一到 UTC 再比对，规避 Local / 固定时区在 reflect.DeepEqual 下的位置指针差异。
func normTime(v dto.VideoInfo) dto.VideoInfo {
	v.CreatedAt = v.CreatedAt.UTC()
	return v
}

func doGet(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestVideoReadHandlerListAndDetail 覆盖网关读路径：列表、详情、404，并与数据库直查结果比对。
func TestVideoReadHandlerListAndDetail(t *testing.T) {
	db := newTestDB(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	repo := videorepo.New(db)

	// 真实 video gRPC server：用真实 MySQL，注册到 Etcd 的唯一服务名。
	srv := grpc.NewServer()
	videov1.RegisterVideoServiceServer(srv, videohandler.NewGRPCVideoServer(videosvc.New(repo, nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, nil)))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	serviceName := fmt.Sprintf("it-gateway-video-%d", time.Now().UnixNano())
	deregister, err := reg.Register(context.Background(), serviceName, lis.Addr().String())
	if err != nil {
		t.Fatalf("注册服务实例失败: %v", err)
	}
	t.Cleanup(deregister)

	conn, err := grpc.NewClient("etcd:///"+serviceName, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := videov1.NewVideoServiceClient(conn)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewVideoReadHandler(client)
	r.GET("/videos", h.List)
	r.GET("/videos/:id", h.Detail)

	ctx := context.Background()

	// -------- 列表：GET /videos?page=1&page_size=5 --------
	var wantList []videomodel.Video
	var wantTotal int64
	wantList, wantTotal, err = repo.ListPublished(ctx, 0, "", videorepo.OrderLatest, 0, 5)
	if err != nil {
		t.Fatalf("数据库直查列表失败: %v", err)
	}

	w := doGet(r, "/videos?page=1&page_size=5")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /videos HTTP 状态 %d", w.Code)
	}
	var listEnv listEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &listEnv); err != nil {
		t.Fatalf("解析列表响应失败: %v, body=%s", err, w.Body.String())
	}
	if listEnv.Code != 0 {
		t.Fatalf("列表返回业务码 %d, body=%s", listEnv.Code, w.Body.String())
	}
	if listEnv.Data.Total != wantTotal {
		t.Fatalf("列表 total=%d, 数据库直查 %d", listEnv.Data.Total, wantTotal)
	}
	if listEnv.Data.Page != 1 || listEnv.Data.PageSize != 5 {
		t.Fatalf("分页字段 page=%d page_size=%d, 期望 1/5", listEnv.Data.Page, listEnv.Data.PageSize)
	}
	if len(listEnv.Data.List) != len(wantList) {
		t.Fatalf("列表条数 HTTP=%d, 数据库=%d", len(listEnv.Data.List), len(wantList))
	}
	for i := range wantList {
		want := normTime(expectedInfo(wantList[i]))
		got := normTime(listEnv.Data.List[i])
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("列表第 %d 项与数据库不一致:\nDB  : %+v\nHTTP: %+v", i, want, got)
		}
	}

	// -------- 详情：GET /videos/{真实 id} --------
	if len(wantList) == 0 {
		t.Skip("库中没有已上架视频，跳过详情比对")
	}
	realID := wantList[0].ID

	before, err := repo.GetByID(ctx, realID)
	if err != nil {
		t.Fatalf("详情前直查失败: %v", err)
	}

	wd := doGet(r, fmt.Sprintf("/videos/%d", realID))
	if wd.Code != http.StatusOK {
		t.Fatalf("GET /videos/%d HTTP 状态 %d", realID, wd.Code)
	}
	var detEnv detailEnvelope
	if err := json.Unmarshal(wd.Body.Bytes(), &detEnv); err != nil {
		t.Fatalf("解析详情响应失败: %v, body=%s", err, wd.Body.String())
	}
	if detEnv.Code != 0 {
		t.Fatalf("详情返回业务码 %d, body=%s", detEnv.Code, wd.Body.String())
	}

	// 详情会自增播放量：期望值为直查值 + 1。
	wantDet := normTime(expectedInfo(*before))
	wantDet.ViewCount = before.ViewCount + 1
	gotDet := normTime(detEnv.Data)
	if !reflect.DeepEqual(wantDet, gotDet) {
		t.Fatalf("详情与数据库不一致（含 +1 播放量）:\nDB+1: %+v\nHTTP: %+v", wantDet, gotDet)
	}

	// 落库校验：数据库里的播放量确实 +1。
	after, err := repo.GetByID(ctx, realID)
	if err != nil {
		t.Fatalf("详情后直查失败: %v", err)
	}
	if after.ViewCount != before.ViewCount+1 {
		t.Fatalf("播放量未自增: %d -> %d", before.ViewCount, after.ViewCount)
	}

	// -------- 404：不存在的 id --------
	wnf := doGet(r, "/videos/99999999")
	if wnf.Code != http.StatusNotFound {
		t.Fatalf("不存在视频 HTTP 状态 %d, 期望 404", wnf.Code)
	}
	var nf codeEnvelope
	if err := json.Unmarshal(wnf.Body.Bytes(), &nf); err != nil {
		t.Fatalf("解析 404 响应失败: %v, body=%s", err, wnf.Body.String())
	}
	if nf.Code != 30001 {
		t.Fatalf("不存在视频业务码 %d, 期望 30001", nf.Code)
	}
}
