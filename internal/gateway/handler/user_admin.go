package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// UserAdminHandler 是管理端用户管理的网关处理器，经 gRPC 客户端访问用户服务。
//
// 与 UserHandler 分开：后者是「本人资料」的自我管理，路由挂 /users/me；
// 这里是管理员对任意账号的操作，路由挂 /admin/users（外层已做 Auth + RequireRole）。
type UserAdminHandler struct {
	client userv1.UserServiceClient
}

// NewUserAdminHandler 构造 UserAdminHandler。
func NewUserAdminHandler(client userv1.UserServiceClient) *UserAdminHandler {
	return &UserAdminHandler{client: client}
}

// AdminList 分页查询全部用户：GET /api/v1/admin/users
//
// 查询参数：page / page_size / keyword（用户名、邮箱、昵称子串）/ role / status。
// role 与 status 只在显式传值时才过滤——不传代表「全部」，不能拿 0 当默认值。
func (h *UserAdminHandler) AdminList(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	role, err := parseSwitchQuery(c.Query("role"))
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的角色过滤（0 普通用户 / 1 管理员）")
		return
	}
	status, err := parseSwitchQuery(c.Query("status"))
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的状态过滤（0 已禁用 / 1 正常）")
		return
	}

	resp, err := h.client.AdminListUsers(c.Request.Context(), &userv1.AdminListUsersRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
		Keyword:  strings.TrimSpace(c.Query("keyword")),
		Role:     toProtoInt32(role),
		Status:   toProtoInt32(status),
	})
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}

	list := make([]dto.UserInfo, 0, len(resp.GetUsers()))
	for _, u := range resp.GetUsers() {
		list = append(list, toUserInfoDTO(u))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// AdminUpdate 修改指定用户的账号状态 / 角色：PATCH /api/v1/admin/users/{id}
//
// 请求体 {"status": 0|1, "role": 0|1}，两个字段都可选，但至少给一个。
// 不允许改自己（服务端会拒），避免管理员把自己降级或封禁后锁在门外。
func (h *UserAdminHandler) AdminUpdate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的用户 ID")
		return
	}

	var in dto.AdminUpdateUserRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, err.Error())
		return
	}

	resp, err := h.client.AdminUpdateUser(c.Request.Context(), &userv1.AdminUpdateUserRequest{
		Id:     id,
		Status: toProtoInt32(in.Status),
		Role:   toProtoInt32(in.Role),
	})
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	response.OK(c, toUserInfoDTO(resp.GetUser()))
}

// parseSwitchQuery 解析只有 0 / 1 两种取值的查询参数；空串表示不过滤。
//
// 不用 0 兜底：「非法参数」与「过滤 0」是两回事，静默兜底会让写错参数的人
// 以为自己看到的是全部用户。与视频管理的 parseStatusQuery 同一取向。
func parseSwitchQuery(raw string) (*int8, error) {
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 8)
	if err != nil || (v != 0 && v != 1) {
		return nil, errcode.ErrInvalidParam
	}
	switchValue := int8(v)
	return &switchValue, nil
}
