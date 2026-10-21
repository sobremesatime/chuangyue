//go:build integration

// 用户服务 gRPC 入口的集成测试：需要本地 MySQL、Redis 与 Etcd。
//
// 覆盖「实例注册 -> 客户端经 etcd 解析 -> 成功发起 RPC」的完整链路：
//   - UserService：批量查询边界语义、资料读 / 改 / 改密、头像客户端流式；
//   - AuthService：发码、注册（含错误码往返）、登录、刷新（含刷新令牌轮换）。
//
// 依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/user/handler/...
package handler

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	usermodel "github.com/sobremesatime/chuangyue/internal/user/model"
	userrepo "github.com/sobremesatime/chuangyue/internal/user/repository"
	"github.com/sobremesatime/chuangyue/internal/user/service"
	"github.com/sobremesatime/chuangyue/pkg/cache"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/jwt"
	"github.com/sobremesatime/chuangyue/pkg/mailer"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/storage"
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

// newTestRedis 连本地 Redis，连不上直接跳过。
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	rdb, err := cache.NewRedis(config.RedisConfig{
		Host:     envOr("REDIS_HOST", "127.0.0.1"),
		Port:     envIntOr("REDIS_PORT", 6379),
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       envIntOr("REDIS_DB", 0),
		PoolSize: 10,
	})
	if err != nil {
		t.Skipf("Redis 不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
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

// uniqueTestService 生成唯一服务名，避免历史残留实例互相干扰。
func uniqueTestService(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// uniqueTestEmail 生成唯一测试邮箱，避免与真实数据及并发用例互相干扰。
func uniqueTestEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano())
}

// testVerifyConfig 是测试使用的验证码策略：与开发配置一致的默认值。
func testVerifyConfig() config.VerifyConfig {
	return config.VerifyConfig{
		CodeTTL:          5 * time.Minute,
		CodeCooldown:     60 * time.Second,
		CodeMaxTry:       5,
		EmailHourlyLimit: 5,
		IPHourlyLimit:    20,
	}
}

// testJWTConfig 是测试使用的令牌配置：有效期固定，便于断言。
func testJWTConfig() config.JWTConfig {
	return config.JWTConfig{
		AccessSecret:  "it-access-secret",
		RefreshSecret: "it-refresh-secret",
		AccessExpire:  15 * time.Minute,
		RefreshExpire: 168 * time.Hour,
		Issuer:        "chuangyue",
	}
}

// testService 用真实 MySQL / Redis / 邮件（log 驱动）构造用户服务。
// localDir 为头像落盘根目录，调用方传 t.TempDir() 避免污染 ./uploads。
func testService(t *testing.T, db *gorm.DB, rdb *redis.Client, localDir string) *service.Service {
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
		jwt.NewManager(testJWTConfig()),
		testVerifyConfig(),
		// 集成测试固定用本地驱动：头像写在 t.TempDir() 下，
		// 断言可以直接按 /uploads/<key> 还原成文件路径检查落盘结果。
		storage.NewLocal(localDir),
	)
}

// startUserServers 起一个真实 gRPC server（注册 UserService 与 AuthService），
// 注册到 Etcd 的唯一服务名，返回两个客户端。
func startUserServers(t *testing.T, reg *registry.Registry, svc *service.Service) (userv1.UserServiceClient, userv1.AuthServiceClient) {
	t.Helper()

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(identity.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(identity.StreamServerInterceptor()),
	)
	userv1.RegisterUserServiceServer(srv, NewGRPCUserServer(svc))
	userv1.RegisterAuthServiceServer(srv, NewGRPCAuthServer(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	serviceName := uniqueTestService("it-user")
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

// firstUser 取一个真实存在的用户，用于校验字段映射；取不到返回 nil。
func firstUser(t *testing.T, db *gorm.DB) *usermodel.User {
	t.Helper()

	var users []usermodel.User
	if err := db.Order("id ASC").Limit(1).Find(&users).Error; err != nil {
		t.Fatalf("查询真实用户失败: %v", err)
	}
	if len(users) == 0 {
		return nil
	}
	return &users[0]
}

// findUser 在返回结果中按 id 查找，找不到返回 nil。
func findUser(users []*userv1.UserBrief, id int64) *userv1.UserBrief {
	for _, u := range users {
		if u.GetId() == id {
			return u
		}
	}
	return nil
}

// TestGRPCUserServerViaEtcd 覆盖用户服务经 Etcd 发现的完整调用链路及边界语义。
func TestGRPCUserServerViaEtcd(t *testing.T) {
	db := newTestDB(t)
	rdb := newTestRedis(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	svc := testService(t, db, rdb, t.TempDir())
	userClient, _ := startUserServers(t, reg, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// ① 真实 id 能取回对应的用户名 / 昵称 / 头像
	if real := firstUser(t, db); real != nil {
		resp, err := userClient.GetUsers(ctx, &userv1.GetUsersRequest{Ids: []int64{real.ID}})
		if err != nil {
			t.Fatalf("GetUsers(%d) 失败: %v", real.ID, err)
		}
		got := findUser(resp.GetUsers(), real.ID)
		if got == nil {
			t.Fatalf("GetUsers(%d) 未返回该用户", real.ID)
		}
		if got.GetUsername() != real.Username || got.GetNickname() != real.Nickname || got.GetAvatar() != real.Avatar {
			t.Fatalf("用户字段映射不一致: got=%+v, want username=%q nickname=%q avatar=%q",
				got, real.Username, real.Nickname, real.Avatar)
		}
	}

	// ② 空 ids 返回空列表且无错误
	empty, err := userClient.GetUsers(ctx, &userv1.GetUsersRequest{})
	if err != nil {
		t.Fatalf("GetUsers(空) 失败: %v", err)
	}
	if len(empty.GetUsers()) != 0 {
		t.Fatalf("GetUsers(空) 应返回空列表, 实际 %d 条", len(empty.GetUsers()))
	}

	// ③ 不存在的 id（0 与一个极大 id）不报错、不 panic，且不出现在结果里
	absent := []int64{0, 1 << 62}
	resp, err := userClient.GetUsers(ctx, &userv1.GetUsersRequest{Ids: absent})
	if err != nil {
		t.Fatalf("GetUsers(不存在) 失败: %v", err)
	}
	for _, u := range resp.GetUsers() {
		if u.GetId() == 0 || u.GetId() == 1<<62 {
			t.Fatalf("返回了不存在的用户: %+v", u)
		}
	}
}

// TestGRPCAuthAndProfileViaEtcd 覆盖认证与资料链路：
// 发码 / 注册（含错误码往返）/ 登录 / 刷新轮换 / 资料读改 / 改密 / 头像流式。
func TestGRPCAuthAndProfileViaEtcd(t *testing.T) {
	db := newTestDB(t)
	rdb := newTestRedis(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	repo := userrepo.New(db)
	codes := userrepo.NewCodeStore(rdb)
	localDir := t.TempDir()
	svc := testService(t, db, rdb, localDir)
	userClient, authClient := startUserServers(t, reg, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// -------- ① 发码：返回值与策略一致 --------
	codeEmail := uniqueTestEmail("it-code")
	codeResp, err := authClient.SendCode(ctx, &userv1.SendCodeRequest{
		Email: codeEmail, Ip: "127.0.0.1", Purpose: "register",
	})
	if err != nil {
		t.Fatalf("SendCode 失败: %v", err)
	}
	vcfg := testVerifyConfig()
	if codeResp.GetExpiresIn() != int64(vcfg.CodeTTL.Seconds()) || codeResp.GetCooldown() != int64(vcfg.CodeCooldown.Seconds()) {
		t.Fatalf("发码返回值 expires_in=%d cooldown=%d, 期望 %d/%d",
			codeResp.GetExpiresIn(), codeResp.GetCooldown(),
			int64(vcfg.CodeTTL.Seconds()), int64(vcfg.CodeCooldown.Seconds()))
	}
	t.Cleanup(func() { _ = codes.Release(context.Background(), "register", codeEmail) })

	// -------- ② 错误码往返：从未发码即注册 -> 20008 --------
	badCodeEmail := uniqueTestEmail("it-badcode")
	_, err = authClient.Register(ctx, &userv1.RegisterRequest{
		Email: badCodeEmail, Code: "000000", Password: "secret123", Nickname: "错误码",
	})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrCodeInvalid.Code {
		t.Fatalf("非法验证码应返回 %d，实际 err=%v", errcode.ErrCodeInvalid.Code, err)
	}

	// -------- ③ 注册成功：直接写入已知验证码，再带码注册 --------
	email := uniqueTestEmail("it-register")
	policy := userrepo.CodePolicy{
		TTL:          vcfg.CodeTTL,
		Cooldown:     vcfg.CodeCooldown,
		MaxTry:       vcfg.CodeMaxTry,
		EmailHourCap: vcfg.EmailHourlyLimit,
		IPHourCap:    vcfg.IPHourlyLimit,
	}
	const knownCode = "654321"
	if err := codes.Issue(ctx, "register", email, "127.0.0.1", knownCode, policy); err != nil {
		t.Fatalf("预置验证码失败: %v", err)
	}
	t.Cleanup(func() { _ = codes.Release(context.Background(), "register", email) })

	const password = "secret123"
	regResp, err := authClient.Register(ctx, &userv1.RegisterRequest{
		Email: email, Code: knownCode, Password: password, Nickname: "集成测试用户",
	})
	if err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	user := regResp.GetUser()
	if user.GetId() == 0 || user.GetEmail() != email || user.GetNickname() != "集成测试用户" {
		t.Fatalf("注册返回值不符: %+v", user)
	}
	t.Cleanup(func() { db.Delete(&usermodel.User{}, user.GetId()) })

	// -------- ④ 登录：错误密码 -> 20002；正确密码返回双令牌 --------
	_, err = authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: "wrong-password"})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrInvalidLogin.Code {
		t.Fatalf("错误密码应返回 %d，实际 err=%v", errcode.ErrInvalidLogin.Code, err)
	}

	loginResp, err := authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: password})
	if err != nil {
		t.Fatalf("Login 失败: %v", err)
	}
	if loginResp.GetAccessToken() == "" || loginResp.GetRefreshToken() == "" || loginResp.GetTokenType() != "Bearer" {
		t.Fatalf("登录返回令牌不完整: %+v", loginResp)
	}
	if want := int64(testJWTConfig().RefreshExpire.Seconds()); loginResp.GetRefreshExpiresIn() != want {
		t.Fatalf("refresh_expires_in=%d, 期望 %d", loginResp.GetRefreshExpiresIn(), want)
	}

	// -------- ⑤ 刷新：返回新访问令牌且刷新令牌被轮换 --------
	// 两枚令牌一定不同：每枚都带随机 jti（重放检测的判据），不再依赖 IssuedAt 跨秒。
	refreshResp, err := authClient.Refresh(ctx, &userv1.RefreshRequest{RefreshToken: loginResp.GetRefreshToken()})
	if err != nil {
		t.Fatalf("Refresh 失败: %v", err)
	}
	if refreshResp.GetAccessToken() == "" || refreshResp.GetRefreshToken() == "" {
		t.Fatalf("刷新返回令牌不完整: %+v", refreshResp)
	}
	if refreshResp.GetRefreshToken() == loginResp.GetRefreshToken() {
		t.Fatalf("刷新令牌应被轮换，实际未变")
	}

	// -------- ⑥ 资料读取：与数据库直查一致 --------
	// 身份经 ctx 透传（客户端拦截器写入 metadata，服务端拦截器取回），不再放进请求字段。
	userCtx := identity.WithUser(ctx, identity.User{ID: user.GetId(), Role: identity.RoleUser})
	profResp, err := userClient.GetProfile(userCtx, &userv1.GetProfileRequest{})
	if err != nil {
		t.Fatalf("GetProfile 失败: %v", err)
	}
	dbUser, err := repo.GetByID(ctx, user.GetId())
	if err != nil {
		t.Fatalf("数据库直查用户失败: %v", err)
	}
	gotProf := profResp.GetUser()
	if gotProf.GetUsername() != dbUser.Username || gotProf.GetEmail() != dbUser.Email ||
		gotProf.GetNickname() != dbUser.Nickname || int8(gotProf.GetRole()) != dbUser.Role {
		t.Fatalf("资料与数据库不一致: HTTP=%+v DB=%+v", gotProf, dbUser)
	}

	// 不存在的用户 -> 20003
	ghostCtx := identity.WithUser(ctx, identity.User{ID: 1 << 62, Role: identity.RoleUser})
	_, err = userClient.GetProfile(ghostCtx, &userv1.GetProfileRequest{})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrUserNotFound.Code {
		t.Fatalf("不存在用户应返回 %d，实际 err=%v", errcode.ErrUserNotFound.Code, err)
	}

	// -------- ⑦ 资料更新：改昵称与生日（含清空）--------
	newNickname := "改名后的昵称"
	birthday := "2000-01-02"
	updResp, err := userClient.UpdateProfile(userCtx, &userv1.UpdateProfileRequest{
		Nickname: &newNickname,
		Birthday: &birthday,
	})
	if err != nil {
		t.Fatalf("UpdateProfile 失败: %v", err)
	}
	if updResp.GetUser().GetNickname() != newNickname || updResp.GetUser().GetBirthday() != birthday {
		t.Fatalf("更新返回不符: %+v", updResp.GetUser())
	}
	if updResp.GetUser().GetAge() <= 0 {
		t.Fatalf("生日已填写，年龄应大于 0，实际 %d", updResp.GetUser().GetAge())
	}

	// 清空生日：传空串
	emptyBirthday := ""
	clrResp, err := userClient.UpdateProfile(userCtx, &userv1.UpdateProfileRequest{
		Birthday: &emptyBirthday,
	})
	if err != nil {
		t.Fatalf("清空生日失败: %v", err)
	}
	if clrResp.GetUser().GetBirthday() != "" || clrResp.GetUser().GetAge() != 0 {
		t.Fatalf("生日应被清空且年龄归零，实际 birthday=%q age=%d",
			clrResp.GetUser().GetBirthday(), clrResp.GetUser().GetAge())
	}

	// 非法昵称保留服务端的中文提示
	tooLong := strings.Repeat("字", 33)
	_, err = userClient.UpdateProfile(userCtx, &userv1.UpdateProfileRequest{Nickname: &tooLong})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Message != "昵称不能超过 32 个字符" {
		t.Fatalf("非法昵称应返回中文提示，实际 err=%v", err)
	}

	// -------- ⑧ 改密：旧密码错误 -> 20010；正确则成功并可用新密码登录 --------
	_, err = userClient.ChangePassword(userCtx, &userv1.ChangePasswordRequest{
		OldPassword: "wrong-old", NewPassword: "newsecret123",
	})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrOldPasswordWrong.Code {
		t.Fatalf("旧密码错误应返回 %d，实际 err=%v", errcode.ErrOldPasswordWrong.Code, err)
	}

	const newPassword = "newsecret123"
	if _, err := userClient.ChangePassword(userCtx, &userv1.ChangePasswordRequest{
		OldPassword: password, NewPassword: newPassword,
	}); err != nil {
		t.Fatalf("ChangePassword 失败: %v", err)
	}
	if _, err := authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: newPassword}); err != nil {
		t.Fatalf("新密码登录失败: %v", err)
	}

	// -------- ⑨ 头像：客户端流式上传后 avatar 落到 /uploads/ --------
	stream, err := userClient.UploadAvatar(userCtx)
	if err != nil {
		t.Fatalf("UploadAvatar 建流失败: %v", err)
	}
	const avatarName = "avatar.png"
	avatarBytes := []byte("fake-png-payload")
	if err := stream.Send(&userv1.UploadAvatarFrame{Payload: &userv1.UploadAvatarFrame_Meta{
		Meta: &userv1.UploadAvatarMeta{Filename: avatarName, Size: int64(len(avatarBytes))},
	}}); err != nil {
		t.Fatalf("发送头像元信息失败: %v", err)
	}
	if err := stream.Send(&userv1.UploadAvatarFrame{Payload: &userv1.UploadAvatarFrame_Chunk{Chunk: avatarBytes}}); err != nil {
		t.Fatalf("发送头像字节失败: %v", err)
	}
	avResp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("UploadAvatar 收尾失败: %v", err)
	}
	avatarURL := avResp.GetUser().GetAvatar()
	if !strings.HasPrefix(avatarURL, "/uploads/") {
		t.Fatalf("头像 URL 前缀不符: %q", avatarURL)
	}
	if _, err := os.Stat(filepath.Join(localDir, strings.TrimPrefix(avatarURL, "/uploads/"))); err != nil {
		t.Fatalf("头像文件未落盘: %v", err)
	}

	// -------- ⑩ 忘记密码：防枚举 + 重置 + 旧刷新令牌立即失效 --------
	// ⑩-1 未注册的邮箱：必须返回与已注册完全相同的成功响应，否则这个接口
	// 就成了「批量探测哪些邮箱注册过」的工具。
	ghostEmail := uniqueTestEmail("it-reset-ghost")
	ghostResp, err := authClient.SendCode(ctx, &userv1.SendCodeRequest{
		Email: ghostEmail, Ip: "127.0.0.1", Purpose: "reset",
	})
	if err != nil {
		t.Fatalf("未注册邮箱请求重置码应返回成功，实际 err=%v", err)
	}
	if ghostResp.GetExpiresIn() != int64(vcfg.CodeTTL.Seconds()) ||
		ghostResp.GetCooldown() != int64(vcfg.CodeCooldown.Seconds()) {
		t.Fatalf("未注册邮箱的返回值与已注册不一致: %+v", ghostResp)
	}
	// 未注册分支不发信，但**照样写码并占用冷却与小时配额**——限流行为必须与已注册一致，
	// 否则「打得久了会限流」本身就成了一个区分邮箱是否注册过的侧信道。
	// 那枚码没有任何送达出口（没发信），因此即使留着也无人可用；下面断言它确实用不了。
	ghostCode := rdb.Get(ctx, "auth:code:reset:"+ghostEmail).Val()
	if ghostCode == "" {
		t.Fatalf("未注册邮箱也应写入验证码（用于统一限流口径）: %s", ghostEmail)
	}
	if _, err := authClient.ResetPassword(ctx, &userv1.ResetPasswordRequest{
		Email: ghostEmail, Code: ghostCode, NewPassword: "whatever123",
	}); err == nil {
		t.Fatalf("未注册邮箱的重置码不应能重置任何账号")
	}
	t.Cleanup(func() { _ = codes.Release(context.Background(), "reset", ghostEmail) })

	// ⑩-2 已注册邮箱：发码成功，测试用的 log 驱动不真发信，从 Redis 里取回那枚码。
	// 走真实发码（而不是预置）是为了覆盖 purpose=reset 这条分支。
	sendResetResp, err := authClient.SendCode(ctx, &userv1.SendCodeRequest{
		Email: email, Ip: "127.0.0.1", Purpose: "reset",
	})
	if err != nil {
		t.Fatalf("请求重置码失败: %v", err)
	}
	if sendResetResp.GetCooldown() != int64(vcfg.CodeCooldown.Seconds()) {
		t.Fatalf("重置码 cooldown=%d, 期望 %d", sendResetResp.GetCooldown(), int64(vcfg.CodeCooldown.Seconds()))
	}
	resetCode := rdb.Get(ctx, "auth:code:reset:"+email).Val()
	if resetCode == "" {
		t.Fatalf("重置验证码未写入 Redis")
	}
	t.Cleanup(func() { _ = codes.Release(context.Background(), "reset", email) })

	// ⑩-3 重置前先留一枚「旧刷新令牌」，用于验证它随后会被吊销。
	// 这里刻意重新登录一次：⑧ 改密不改令牌版本，所以此刻的登录态仍是「重置前」的。
	preResetLogin, err := authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: newPassword})
	if err != nil {
		t.Fatalf("重置前登录失败: %v", err)
	}

	// ⑩-4 未登录、无旧密码即可重置（这正是「忘记密码」与「修改密码」的分水岭）。
	const resetPassword = "resetsecret456"
	if _, err := authClient.ResetPassword(ctx, &userv1.ResetPasswordRequest{
		Email: email, Code: resetCode, NewPassword: resetPassword,
	}); err != nil {
		t.Fatalf("ResetPassword 失败: %v", err)
	}

	// 验证码是一次性凭证：同一枚码不能再用第二次。
	_, err = authClient.ResetPassword(ctx, &userv1.ResetPasswordRequest{
		Email: email, Code: resetCode, NewPassword: "another-secret",
	})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrCodeInvalid.Code {
		t.Fatalf("验证码应已核销，期望 %d，实际 err=%v", errcode.ErrCodeInvalid.Code, err)
	}

	// ⑩-5 新密码可用、旧密码失效。
	if _, err := authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: resetPassword}); err != nil {
		t.Fatalf("重置后用新密码登录失败: %v", err)
	}
	_, err = authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: newPassword})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrInvalidLogin.Code {
		t.Fatalf("重置后旧密码应失效，期望 %d，实际 err=%v", errcode.ErrInvalidLogin.Code, err)
	}

	// ⑩-6 关键断言：重置前签发的刷新令牌立刻被吊销（token_version 已 +1）。
	_, err = authClient.Refresh(ctx, &userv1.RefreshRequest{RefreshToken: preResetLogin.GetRefreshToken()})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrTokenInvalid.Code {
		t.Fatalf("重置前的刷新令牌应被吊销，期望 %d，实际 err=%v", errcode.ErrTokenInvalid.Code, err)
	}

	// -------- ⑪ 刷新令牌重放检测：同一枚用第二次 -> 吊销全部会话 --------
	// ⑪-1 登录并正常刷新一次（R1 -> R2）：R1 被轮换掉，同时应在 Redis 留下「用过」标记。
	replayLogin, err := authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: resetPassword})
	if err != nil {
		t.Fatalf("重放检测用例登录失败: %v", err)
	}
	rotated, err := authClient.Refresh(ctx, &userv1.RefreshRequest{RefreshToken: replayLogin.GetRefreshToken()})
	if err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}
	if rotated.GetRefreshToken() == replayLogin.GetRefreshToken() {
		t.Fatalf("刷新令牌应被轮换")
	}

	r1Claims, err := jwt.NewManager(testJWTConfig()).ParseRefreshToken(replayLogin.GetRefreshToken())
	if err != nil {
		t.Fatalf("解析轮换前的刷新令牌失败: %v", err)
	}
	if r1Claims.ID == "" {
		t.Fatalf("刷新令牌应带 jti")
	}
	// 标记必须带正 TTL：这些键靠令牌自身剩余有效期自动清理，不留清理任务。
	usedKey := "auth:rt:used:" + r1Claims.ID
	if ttl := rdb.TTL(ctx, usedKey).Val(); ttl <= 0 {
		t.Fatalf("重放标记 %s 应带正 TTL，实际 %v", usedKey, ttl)
	}
	t.Cleanup(func() { _ = rdb.Del(context.Background(), usedKey).Err() })

	// ⑪-2 重放 R1（已被轮换掉的那枚）-> 拒绝。
	_, err = authClient.Refresh(ctx, &userv1.RefreshRequest{RefreshToken: replayLogin.GetRefreshToken()})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrTokenInvalid.Code {
		t.Fatalf("重放应被拒绝，期望 %d，实际 err=%v", errcode.ErrTokenInvalid.Code, err)
	}

	// ⑪-3 关键断言：连**攻击者手里那枚**「轮换后的新令牌」也必须一起失效。
	// 只拒绝重放是不够的——R1 被偷的那一刻，攻击者多半已经拿它换到了 R2；
	// 靠 token_version +1 才能把 R2 一并作废。
	_, err = authClient.Refresh(ctx, &userv1.RefreshRequest{RefreshToken: rotated.GetRefreshToken()})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrTokenInvalid.Code {
		t.Fatalf("重放后全部会话应被吊销，期望 %d，实际 err=%v", errcode.ErrTokenInvalid.Code, err)
	}

	// ⑪-4 吊销是「全端一次性下线」而非封禁：重新登录后一切照常。
	relogin, err := authClient.Login(ctx, &userv1.LoginRequest{Account: email, Password: resetPassword})
	if err != nil {
		t.Fatalf("重放吊销后重新登录失败: %v", err)
	}
	if _, err := authClient.Refresh(ctx, &userv1.RefreshRequest{RefreshToken: relogin.GetRefreshToken()}); err != nil {
		t.Fatalf("重新登录后刷新失败: %v", err)
	}
}
