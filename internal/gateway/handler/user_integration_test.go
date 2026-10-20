//go:build integration

// 网关用户与认证路径的集成测试：起真实 user gRPC server（真实 MySQL / Redis / 邮件 / 令牌，
// 注册到 Etcd），用 etcd:/// 解析出的客户端 + httptest 起的 gin 路由走完整链路，
// 断言 HTTP 响应 JSON、Cookie 与业务错误码与改造前逐字一致。
//
// 依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/gateway/handler/...
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	"github.com/sobremesatime/chuangyue/internal/user/dto"
	userhandler "github.com/sobremesatime/chuangyue/internal/user/handler"
	usermodel "github.com/sobremesatime/chuangyue/internal/user/model"
	userrepo "github.com/sobremesatime/chuangyue/internal/user/repository"
	"github.com/sobremesatime/chuangyue/internal/user/service"
	"github.com/sobremesatime/chuangyue/pkg/cache"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/jwt"
	"github.com/sobremesatime/chuangyue/pkg/mailer"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// newTestRedis 连本地 Redis，连不上直接跳过。
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	rdb, err := cache.NewRedis(config.RedisConfig{
		Host:     envOr("REDIS_HOST", "127.0.0.1"),
		Port:     envIntOr("REDIS_PORT", 6379),
		Password: "",
		DB:       envIntOr("REDIS_DB", 0),
		PoolSize: 10,
	})
	if err != nil {
		t.Skipf("Redis 不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// testJWTConfig 是测试使用的令牌配置：与用户服务侧一致，保证网关验签通过。
func testJWTConfig() config.JWTConfig {
	return config.JWTConfig{
		AccessSecret:  "it-access-secret",
		RefreshSecret: "it-refresh-secret",
		AccessExpire:  15 * time.Minute,
		RefreshExpire: 168 * time.Hour,
		Issuer:        "chuangyue",
	}
}

// uniqueTestEmail 生成唯一测试邮箱，避免与真实数据及并发用例互相干扰。
func uniqueTestEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano())
}

// testUserService 用真实 MySQL / Redis / 邮件（log 驱动）构造用户服务，头像落在临时目录。
func testUserService(t *testing.T, db *gorm.DB, rdb *redis.Client, localDir string, jwtCfg config.JWTConfig) *service.Service {
	t.Helper()

	mail, err := mailer.New(config.MailConfig{Driver: mailer.DriverLog, From: "窗月 <no-reply@chuangyue.dev>"}, "dev")
	if err != nil {
		t.Fatalf("构造邮件发送器失败: %v", err)
	}
	return service.New(
		userrepo.New(db),
		userrepo.NewCodeStore(rdb),
		userrepo.NewRefreshStore(rdb),
		mail,
		jwt.NewManager(jwtCfg),
		config.VerifyConfig{
			CodeTTL:          5 * time.Minute,
			CodeCooldown:     60 * time.Second,
			CodeMaxTry:       5,
			EmailHourlyLimit: 5,
			IPHourlyLimit:    20,
		},
		config.UploadConfig{
			Driver:     "local",
			LocalDir:   localDir,
			MaxSizeMB:  2,
			AllowedExt: []string{".jpg", ".jpeg", ".png", ".webp"},
		},
	)
}

// startUserGRPCServers 起一个真实 gRPC server（注册 UserService 与 AuthService），
// 注册到 Etcd 的唯一服务名，返回两个客户端。
func startUserGRPCServers(t *testing.T, reg *registry.Registry, svc *service.Service) (userv1.UserServiceClient, userv1.AuthServiceClient) {
	t.Helper()

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(identity.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(identity.StreamServerInterceptor()),
	)
	userv1.RegisterUserServiceServer(srv, userhandler.NewGRPCUserServer(svc))
	userv1.RegisterAuthServiceServer(srv, userhandler.NewGRPCAuthServer(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	serviceName := fmt.Sprintf("it-gateway-user-%d", time.Now().UnixNano())
	deregister, err := reg.Register(context.Background(), serviceName, lis.Addr().String())
	if err != nil {
		t.Fatalf("注册服务实例失败: %v", err)
	}
	t.Cleanup(deregister)

	conn, err := grpc.NewClient("etcd:///"+serviceName,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(identity.UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(identity.StreamClientInterceptor()),
	)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return userv1.NewUserServiceClient(conn), userv1.NewAuthServiceClient(conn)
}

// userEnvelope 对应用户资料接口的响应信封。
type userEnvelope struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    dto.UserInfo `json:"data"`
}

// tokenEnvelope 对应登录 / 刷新接口的响应信封。
type tokenEnvelope struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    dto.TokenPair `json:"data"`
}

// errEnvelope 只取错误码与提示，用于断言错误语义。
type errEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// doJSON 发一个请求，body/hdr 为空时可不传。
func doJSON(r *gin.Engine, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// findCookie 从响应中取出指定名字的 Cookie，不存在返回 nil。
func findCookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, ck := range w.Result().Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

// TestGatewayUserAndAuthViaEtcd 覆盖网关用户 / 认证路径：
// 登录（JSON + Set-Cookie）、/users/me 读改、改密、刷新轮换、登出清 Cookie、404 映射。
func TestGatewayUserAndAuthViaEtcd(t *testing.T) {
	db := newTestDB(t)
	rdb := newTestRedis(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	ctx := context.Background()
	repo := userrepo.New(db)

	// 真实用户 gRPC 服务：与生产一致地持有库 / Redis / 邮件 / 令牌签发。
	jwtCfg := testJWTConfig()
	svc := testUserService(t, db, rdb, t.TempDir(), jwtCfg)
	userClient, authClient := startUserGRPCServers(t, reg, svc)

	// 网关侧只持有两个 gRPC 客户端与 JWT 验签器，路由与生产装配保持一致。
	jwtMgr := jwt.NewManager(jwtCfg)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	authHandler := NewAuthHandler(authClient, false)
	userHandler := NewUserHandler(userClient)

	api := r.Group("/api/v1")
	authGroup := api.Group("/auth")
	authGroup.POST("/login", authHandler.Login)
	authGroup.POST("/refresh", authHandler.Refresh)
	authGroup.POST("/logout", middleware.Auth(jwtMgr), authHandler.Logout)
	api.GET("/users/me", middleware.Auth(jwtMgr), userHandler.Profile)
	api.PATCH("/users/me", middleware.Auth(jwtMgr), userHandler.UpdateProfile)
	api.PUT("/users/me/password", middleware.Auth(jwtMgr), userHandler.ChangePassword)

	// -------- 播种一个已知密码的用户，用于走真实登录链路 -------------
	const password = "gateway123"
	email := uniqueTestEmail("it-gw")
	hash, err := utils.HashPassword(password)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	seed := &usermodel.User{
		ID:           snowflake.NextID(),
		Username:     fmt.Sprintf("gw%d", time.Now().UnixNano()),
		Email:        email,
		PasswordHash: hash,
		Nickname:     "网关用户",
		Role:         usermodel.RoleUser,
		Status:       usermodel.StatusNormal,
	}
	if err := repo.Create(ctx, seed); err != nil {
		t.Fatalf("播种用户失败: %v", err)
	}
	t.Cleanup(func() { db.Delete(&usermodel.User{}, seed.ID) })

	// -------- ① 登录：响应体返回访问令牌，刷新令牌写入 HttpOnly Cookie -------------
	wLogin := doJSON(r, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"account":%q,"password":%q}`, email, password), nil)
	if wLogin.Code != http.StatusOK {
		t.Fatalf("登录 HTTP 状态 %d, body=%s", wLogin.Code, wLogin.Body.String())
	}
	var loginTok tokenEnvelope
	if err := json.Unmarshal(wLogin.Body.Bytes(), &loginTok); err != nil {
		t.Fatalf("解析登录响应失败: %v, body=%s", err, wLogin.Body.String())
	}
	if loginTok.Code != 0 || loginTok.Data.AccessToken == "" || loginTok.Data.TokenType != "Bearer" {
		t.Fatalf("登录返回不符: %+v", loginTok)
	}
	loginCookie := findCookie(wLogin, "refresh_token")
	if loginCookie == nil || loginCookie.Value == "" {
		t.Fatalf("登录未下发 refresh_token Cookie: %+v", loginCookie)
	}
	if !loginCookie.HttpOnly || loginCookie.Path != "/api/v1/auth" {
		t.Fatalf("refresh_token Cookie 属性不符: HttpOnly=%v Path=%q", loginCookie.HttpOnly, loginCookie.Path)
	}
	if loginCookie.MaxAge != int(jwtCfg.RefreshExpire.Seconds()) {
		t.Fatalf("refresh_token Max-Age=%d, 期望 %d", loginCookie.MaxAge, int(jwtCfg.RefreshExpire.Seconds()))
	}
	authHeader := map[string]string{"Authorization": "Bearer " + loginTok.Data.AccessToken}

	// ------------- ② 无令牌访问 /users/me -> 401 / 10002 -------------
	wNoAuth := doJSON(r, http.MethodGet, "/api/v1/users/me", "", nil)
	if wNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌 HTTP 状态 %d, 期望 401", wNoAuth.Code)
	}
	var noAuthErr errEnvelope
	_ = json.Unmarshal(wNoAuth.Body.Bytes(), &noAuthErr)
	if noAuthErr.Code != errcode.ErrUnauthorized.Code {
		t.Fatalf("无令牌业务码 %d, 期望 %d", noAuthErr.Code, errcode.ErrUnauthorized.Code)
	}

	// -------- ③ GET /users/me：与播种数据一致 -------------
	wMe := doJSON(r, http.MethodGet, "/api/v1/users/me", "", authHeader)
	if wMe.Code != http.StatusOK {
		t.Fatalf("GET /users/me HTTP 状态 %d, body=%s", wMe.Code, wMe.Body.String())
	}
	var meEnv userEnvelope
	if err := json.Unmarshal(wMe.Body.Bytes(), &meEnv); err != nil {
		t.Fatalf("解析 /users/me 响应失败: %v, body=%s", err, wMe.Body.String())
	}
	if meEnv.Code != 0 || meEnv.Data.ID != seed.ID || meEnv.Data.Email != email || meEnv.Data.Nickname != "网关用户" {
		t.Fatalf("/users/me 与播种数据不一致: %+v", meEnv.Data)
	}

	// ------------- ④ PATCH /users/me 非法昵称：保留服务端中文提示与 10001 -------------
	wBad := doJSON(r, http.MethodPatch, "/api/v1/users/me",
		fmt.Sprintf(`{"nickname":%q}`, strings.Repeat("字", 33)), authHeader)
	if wBad.Code != http.StatusOK {
		t.Fatalf("非法昵称 HTTP 状态 %d, 期望 200（10001 走默认分支）", wBad.Code)
	}
	var badErr errEnvelope
	if err := json.Unmarshal(wBad.Body.Bytes(), &badErr); err != nil {
		t.Fatalf("解析非法昵称响应失败: %v, body=%s", err, wBad.Body.String())
	}
	if badErr.Code != errcode.ErrInvalidParam.Code || badErr.Message != "昵称不能超过 32 个字符" {
		t.Fatalf("非法昵称响应不符: code=%d message=%q", badErr.Code, badErr.Message)
	}

	// ------------- ⑤ PATCH /users/me 成功：返回更新后的资料 -------------
	wUpd := doJSON(r, http.MethodPatch, "/api/v1/users/me", `{"nickname":"新昵称"}`, authHeader)
	if wUpd.Code != http.StatusOK {
		t.Fatalf("PATCH /users/me HTTP 状态 %d, body=%s", wUpd.Code, wUpd.Body.String())
	}
	var updEnv userEnvelope
	_ = json.Unmarshal(wUpd.Body.Bytes(), &updEnv)
	if updEnv.Code != 0 || updEnv.Data.Nickname != "新昵称" {
		t.Fatalf("更新资料返回不符: %+v", updEnv.Data)
	}

	// ------------- ⑥ 改密：旧密码错误 -> 20010；成功后可用新密码登录 -------------
	wWrongPwd := doJSON(r, http.MethodPut, "/api/v1/users/me/password",
		`{"old_password":"wrong-old","new_password":"newsecret123"}`, authHeader)
	if wWrongPwd.Code != http.StatusOK {
		t.Fatalf("旧密码错误 HTTP 状态 %d, 期望 200", wWrongPwd.Code)
	}
	var wrongPwdErr errEnvelope
	_ = json.Unmarshal(wWrongPwd.Body.Bytes(), &wrongPwdErr)
	if wrongPwdErr.Code != errcode.ErrOldPasswordWrong.Code {
		t.Fatalf("旧密码错误业务码 %d, 期望 %d", wrongPwdErr.Code, errcode.ErrOldPasswordWrong.Code)
	}

	const newPassword = "newsecret123"
	wChg := doJSON(r, http.MethodPut, "/api/v1/users/me/password",
		fmt.Sprintf(`{"old_password":%q,"new_password":%q}`, password, newPassword), authHeader)
	if wChg.Code != http.StatusOK {
		t.Fatalf("改密 HTTP 状态 %d, body=%s", wChg.Code, wChg.Body.String())
	}
	var chgEnv userEnvelope
	_ = json.Unmarshal(wChg.Body.Bytes(), &chgEnv)
	if chgEnv.Code != 0 {
		t.Fatalf("改密返回业务码 %d, body=%s", chgEnv.Code, wChg.Body.String())
	}
	wNewLogin := doJSON(r, http.MethodPost, "/api/v1/auth/login",
		fmt.Sprintf(`{"account":%q,"password":%q}`, email, newPassword), nil)
	var newLoginTok tokenEnvelope
	_ = json.Unmarshal(wNewLogin.Body.Bytes(), &newLoginTok)
	if newLoginTok.Code != 0 || newLoginTok.Data.AccessToken == "" {
		t.Fatalf("新密码登录失败: %+v, body=%s", newLoginTok, wNewLogin.Body.String())
	}

	// ------------- ⑦ 刷新：换新访问令牌且刷新令牌被轮换 -------------
	// JWT 的 IssuedAt 精确到秒，同一秒内签发的刷新令牌字节相同；
	// 这里先跨过一个秒边界，让「轮换」断言稳定（真实场景刷新间隔远大于 1 秒）。
	time.Sleep(time.Second)
	wRefresh := doJSON(r, http.MethodPost, "/api/v1/auth/refresh", "",
		map[string]string{"Cookie": "refresh_token=" + loginCookie.Value})
	if wRefresh.Code != http.StatusOK {
		t.Fatalf("刷新 HTTP 状态 %d, body=%s", wRefresh.Code, wRefresh.Body.String())
	}
	var refreshTok tokenEnvelope
	_ = json.Unmarshal(wRefresh.Body.Bytes(), &refreshTok)
	if refreshTok.Code != 0 || refreshTok.Data.AccessToken == "" {
		t.Fatalf("刷新返回不符: %+v", refreshTok)
	}
	rotated := findCookie(wRefresh, "refresh_token")
	if rotated == nil || rotated.Value == "" || rotated.Value == loginCookie.Value {
		t.Fatalf("刷新令牌应被轮换: old=%q new=%+v", loginCookie.Value, rotated)
	}

	// ------------- ⑧ 登出：清空刷新令牌 Cookie（Max-Age=0，解析为 -1）--------
	wLogout := doJSON(r, http.MethodPost, "/api/v1/auth/logout", "", authHeader)
	if wLogout.Code != http.StatusOK {
		t.Fatalf("登出 HTTP 状态 %d, body=%s", wLogout.Code, wLogout.Body.String())
	}
	var logoutEnv errEnvelope
	_ = json.Unmarshal(wLogout.Body.Bytes(), &logoutEnv)
	if logoutEnv.Code != 0 {
		t.Fatalf("登出返回业务码 %d, body=%s", logoutEnv.Code, wLogout.Body.String())
	}
	cleared := findCookie(wLogout, "refresh_token")
	if cleared == nil || cleared.Value != "" || cleared.MaxAge != -1 {
		t.Fatalf("登出应清空 refresh_token Cookie: %+v", cleared)
	}

	// ------------- ⑨ 404 映射：令牌指向不存在的用户 -> 404 / 20003 -------------
	ghostToken, _, err := jwtMgr.GenerateAccessToken(1<<62, usermodel.RoleUser, 0)
	if err != nil {
		t.Fatalf("签发幽灵令牌失败: %v", err)
	}
	wGhost := doJSON(r, http.MethodGet, "/api/v1/users/me", "",
		map[string]string{"Authorization": "Bearer " + ghostToken})
	if wGhost.Code != http.StatusNotFound {
		t.Fatalf("不存在用户 HTTP 状态 %d, 期望 404", wGhost.Code)
	}
	var ghostErr errEnvelope
	if err := json.Unmarshal(wGhost.Body.Bytes(), &ghostErr); err != nil {
		t.Fatalf("解析 404 响应失败: %v, body=%s", err, wGhost.Body.String())
	}
	if ghostErr.Code != errcode.ErrUserNotFound.Code {
		t.Fatalf("不存在用户业务码 %d, 期望 %d", ghostErr.Code, errcode.ErrUserNotFound.Code)
	}
}
