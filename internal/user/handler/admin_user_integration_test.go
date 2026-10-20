//go:build integration

// 用户管理（管理端）的集成测试：需要本地 MySQL、Redis 与 Etcd。
//
// 覆盖：列表的关键词 / 角色 / 状态过滤与分页、封禁与解封、角色变更、
// 以及「不允许改自己」这条护栏。依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v -run TestGRPCAdminUserManagement ./internal/user/handler/...
package handler

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	usermodel "github.com/sobremesatime/chuangyue/internal/user/model"
	userrepo "github.com/sobremesatime/chuangyue/internal/user/repository"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

func TestGRPCAdminUserManagement(t *testing.T) {
	db := newTestDB(t)
	rdb := newTestRedis(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	repo := userrepo.New(db)
	svc := testService(t, db, rdb, t.TempDir())
	userClient, authClient := startUserServers(t, reg, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 三个账号共用同一段随机后缀，便于用 keyword 把它们从真实数据里筛出来。
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	const password = "secret123"
	hash, err := utils.HashPassword(password)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}

	createUser := func(role, status int8) *usermodel.User {
		t.Helper()
		u := &usermodel.User{
			ID:           snowflake.NextID(),
			Username:     "um" + suffix + strconv.Itoa(int(role)) + strconv.Itoa(int(status)),
			Email:        "um" + suffix + strconv.Itoa(int(role)) + strconv.Itoa(int(status)) + "@example.com",
			PasswordHash: hash,
			Nickname:     "用户管理" + strconv.Itoa(int(role)),
			Role:         role,
			Status:       status,
		}
		if err := repo.Create(ctx, u); err != nil {
			t.Fatalf("创建测试用户失败: %v", err)
		}
		t.Cleanup(func() { db.Delete(&usermodel.User{}, u.ID) })
		return u
	}

	admin := createUser(usermodel.RoleAdmin, usermodel.StatusNormal)
	target := createUser(usermodel.RoleUser, usermodel.StatusNormal)
	banned := createUser(usermodel.RoleUser, usermodel.StatusDisabled)

	// 操作者身份经 gRPC metadata 透传；服务端用它拦住「改自己」。
	adminCtx := identity.WithUser(ctx, identity.User{ID: admin.ID, Role: identity.RoleAdmin})

	// -------- ① 列表：关键词命中这三个账号，总数与分页一致 --------
	listResp, err := userClient.AdminListUsers(adminCtx, &userv1.AdminListUsersRequest{
		Page: 1, PageSize: 20, Keyword: suffix,
	})
	if err != nil {
		t.Fatalf("AdminListUsers 失败: %v", err)
	}
	if listResp.GetTotal() != 3 || len(listResp.GetUsers()) != 3 {
		t.Fatalf("关键词应命中 3 个账号，实际 total=%d len=%d", listResp.GetTotal(), len(listResp.GetUsers()))
	}
	if listResp.GetPage() != 1 || listResp.GetPageSize() != 20 {
		t.Fatalf("分页回显不符: page=%d page_size=%d", listResp.GetPage(), listResp.GetPageSize())
	}

	// -------- ② 列表：角色 / 状态过滤（0 也是合法值，不能被当成「不过滤」）--------
	roleFilter := int32(usermodel.RoleAdmin)
	byRole, err := userClient.AdminListUsers(adminCtx, &userv1.AdminListUsersRequest{
		Keyword: suffix, Role: &roleFilter,
	})
	if err != nil {
		t.Fatalf("按角色过滤失败: %v", err)
	}
	if len(byRole.GetUsers()) != 1 || byRole.GetUsers()[0].GetId() != admin.ID {
		t.Fatalf("按角色=管理员应只剩 admin，实际 %d 条", len(byRole.GetUsers()))
	}

	statusFilter := int32(usermodel.StatusDisabled)
	byStatus, err := userClient.AdminListUsers(adminCtx, &userv1.AdminListUsersRequest{
		Keyword: suffix, Status: &statusFilter,
	})
	if err != nil {
		t.Fatalf("按状态过滤失败: %v", err)
	}
	if len(byStatus.GetUsers()) != 1 || byStatus.GetUsers()[0].GetId() != banned.ID {
		t.Fatalf("按状态=已禁用应只剩 banned，实际 %d 条", len(byStatus.GetUsers()))
	}
	if byStatus.GetUsers()[0].GetStatus() != int32(usermodel.StatusDisabled) {
		t.Fatalf("列表应回传账号状态，实际 status=%d", byStatus.GetUsers()[0].GetStatus())
	}

	// -------- ③ 列表：分页截断 --------
	pageResp, err := userClient.AdminListUsers(adminCtx, &userv1.AdminListUsersRequest{
		Page: 1, PageSize: 2, Keyword: suffix,
	})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(pageResp.GetUsers()) != 2 || pageResp.GetTotal() != 3 {
		t.Fatalf("page_size=2 应返回 2 条、total=3，实际 len=%d total=%d",
			len(pageResp.GetUsers()), pageResp.GetTotal())
	}

	// -------- ④ 自我保护：不能改自己的状态或角色 --------
	bannedStatus := int32(usermodel.StatusDisabled)
	_, err = userClient.AdminUpdateUser(adminCtx, &userv1.AdminUpdateUserRequest{
		Id: admin.ID, Status: &bannedStatus,
	})
	if ec, ok := grpcerr.Unwrap(err); !ok || !strings.Contains(ec.Message, "不能修改自己") {
		t.Fatalf("改自己应被拒绝，实际 err=%v", err)
	}

	// -------- ⑤ 封禁：目标账号立刻无法登录 --------
	banResp, err := userClient.AdminUpdateUser(adminCtx, &userv1.AdminUpdateUserRequest{
		Id: target.ID, Status: &bannedStatus,
	})
	if err != nil {
		t.Fatalf("封禁失败: %v", err)
	}
	if banResp.GetUser().GetStatus() != int32(usermodel.StatusDisabled) {
		t.Fatalf("封禁后状态应为 0，实际 %d", banResp.GetUser().GetStatus())
	}
	_, err = authClient.Login(ctx, &userv1.LoginRequest{Account: target.Username, Password: password})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrUserDisabled.Code {
		t.Fatalf("被禁用的账号登录应返回 %d，实际 err=%v", errcode.ErrUserDisabled.Code, err)
	}

	// -------- ⑥ 解封：恢复登录 --------
	normalStatus := int32(usermodel.StatusNormal)
	if _, err := userClient.AdminUpdateUser(adminCtx, &userv1.AdminUpdateUserRequest{
		Id: target.ID, Status: &normalStatus,
	}); err != nil {
		t.Fatalf("解封失败: %v", err)
	}
	if _, err := authClient.Login(ctx, &userv1.LoginRequest{Account: target.Username, Password: password}); err != nil {
		t.Fatalf("解封后应能登录，实际 err=%v", err)
	}

	// -------- ⑦ 角色变更：升为管理员 --------
	adminRole := int32(usermodel.RoleAdmin)
	promoteResp, err := userClient.AdminUpdateUser(adminCtx, &userv1.AdminUpdateUserRequest{
		Id: target.ID, Role: &adminRole,
	})
	if err != nil {
		t.Fatalf("升权失败: %v", err)
	}
	if promoteResp.GetUser().GetRole() != int32(usermodel.RoleAdmin) {
		t.Fatalf("升权后角色应为 1，实际 %d", promoteResp.GetUser().GetRole())
	}

	// -------- ⑧ 非法入参：越界取值 / 空请求 / 不存在的用户 --------
	invalidStatus := int32(2)
	_, err = userClient.AdminUpdateUser(adminCtx, &userv1.AdminUpdateUserRequest{
		Id: target.ID, Status: &invalidStatus,
	})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrInvalidParam.Code {
		t.Fatalf("越界状态应返回 %d，实际 err=%v", errcode.ErrInvalidParam.Code, err)
	}

	_, err = userClient.AdminUpdateUser(adminCtx, &userv1.AdminUpdateUserRequest{Id: target.ID})
	if ec, ok := grpcerr.Unwrap(err); !ok || !strings.Contains(ec.Message, "没有需要更新的字段") {
		t.Fatalf("空请求应被拒绝，实际 err=%v", err)
	}

	_, err = userClient.AdminUpdateUser(adminCtx, &userv1.AdminUpdateUserRequest{
		Id: 1 << 62, Status: &normalStatus,
	})
	if ec, ok := grpcerr.Unwrap(err); !ok || ec.Code != errcode.ErrUserNotFound.Code {
		t.Fatalf("不存在的用户应返回 %d，实际 err=%v", errcode.ErrUserNotFound.Code, err)
	}
}
