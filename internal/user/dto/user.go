// Package dto 定义用户域的请求与响应结构，字段与 api/openapi/chuangyue.v1.yaml 保持一致。
package dto

import (
	"io"
	"time"
)

// RegisterRequest 注册请求。
//
// 注册以邮箱为唯一身份：先用邮箱换取验证码，再带码提交。
// 用户名不在这里传入，由服务端从邮箱前缀派生。
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email,max=128"`
	Code     string `json:"code" binding:"required,len=6,numeric"`
	Password string `json:"password" binding:"required,min=6,max=64"`
	Nickname string `json:"nickname" binding:"max=32"`
}

// LoginRequest 登录请求。
//
// Account 同时支持用户名与邮箱：含 @ 按邮箱查，否则按用户名查。
type LoginRequest struct {
	Account  string `json:"account" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// ChangePasswordRequest 修改密码请求。
//
// 不在这里校验长度：与 UpdateProfileRequest 一致，把带中文提示的校验放在 service 层，
// 这里只保证两个字段都出现。
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// SendCodeRequest 发送邮箱验证码请求。
//
// purpose 决定这枚码用来做什么，两条链路的语义不同：
// register 在邮箱已注册时报错；reset 无论是否注册都返回成功（防邮箱枚举）。
type SendCodeRequest struct {
	Email   string `json:"email" binding:"required"`
	Purpose string `json:"purpose" binding:"required,oneof=register reset"`
}

// SendCodeResult 发送邮箱验证码的结果，供前端做重发倒计时。
type SendCodeResult struct {
	ExpiresIn int64 `json:"expires_in"`
	Cooldown  int64 `json:"cooldown"`
}

// ResetPasswordRequest 重置密码请求（忘记密码通道）。
//
// 不需要登录、也没有 old_password：持有一枚发给该邮箱的一次性验证码，
// 就足以证明邮箱归属，这正是「忘记密码」与「修改密码」的分水岭。
type ResetPasswordRequest struct {
	Email       string `json:"email" binding:"required,email,max=128"`
	Code        string `json:"code" binding:"required,len=6,numeric"`
	NewPassword string `json:"new_password" binding:"required"`
}

// AdminListUsersRequest 是用户管理列表的查询条件。
//
// Role / Status 用指针：nil 表示不过滤。不能拿 0 当「不过滤」——
// 0 既是「普通用户」也是「已禁用」，那样会静默变成「只看普通用户」。
type AdminListUsersRequest struct {
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Keyword  string `json:"keyword"`
	Role     *int8  `json:"role"`
	Status   *int8  `json:"status"`
}

// UserPage 是用户管理列表的一页。
type UserPage struct {
	Users    []UserInfo `json:"users"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

// AdminUpdateUserRequest 修改指定用户的账号状态 / 角色。
//
// 两个字段都可选（nil = 不改），但至少要有一个，否则视为无意义请求。
type AdminUpdateUserRequest struct {
	Status *int8 `json:"status"`
	Role   *int8 `json:"role"`
}

// UserInfo 对外暴露的用户信息，绝不包含密码哈希。
//
// Birthday 以 YYYY-MM-DD 字符串对外（未填写为空串），Age 由生日实时推算，
// 两者都是给人看的展示值，落库的只有 birthday 一列。
type UserInfo struct {
	// ID 以字符串序列化：雪花 ID 超出 JavaScript 安全整数上限，按数字传输会导致前端精度丢失。
	ID        int64  `json:"id,string"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Nickname  string `json:"nickname"`
	Avatar    string `json:"avatar"`
	Signature string `json:"signature"`
	Gender    int8   `json:"gender"`
	Birthday  string `json:"birthday"`
	Age       int    `json:"age"`
	School    string `json:"school"`
	Location  string `json:"location"`
	Role      int8   `json:"role"`
	// Status 是账号状态（1 正常 / 0 已禁用）。本人资料里恒为 1，
	// 管理端列表用它显示封禁态。
	Status    int8      `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// UploadAvatarInput 是上传头像的输入。
//
// 与视频上传一致，用 io.Reader 而不是 *multipart.FileHeader，
// 让 service 层不依赖 gin，后续可被 gRPC 接口复用。
type UploadAvatarInput struct {
	Filename string
	Size     int64
	File     io.Reader
}

// UpdateProfileRequest 更新个人资料。
//
// 字段一律用指针：nil 表示「本次不修改」，指向空串表示「清空该字段」。
// 校验统一放在 service 层做（便于返回中文提示），因此这里不加 binding 标签。
//
// Username 是登录凭据之一，改成功后旧用户名立即失效（邮箱仍可登录）。
type UpdateProfileRequest struct {
	Username  *string `json:"username"`
	Nickname  *string `json:"nickname"`
	Signature *string `json:"signature"`
	Gender    *int8   `json:"gender"`
	Birthday  *string `json:"birthday"`
	School    *string `json:"school"`
	Location  *string `json:"location"`
}

// TokenPair 是登录 / 刷新成功时返回的访问令牌信息。
// 刷新令牌不放在响应体中，而是写入 HttpOnly Cookie，避免 XSS 窃取。
type TokenPair struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}
