package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

const (
	// refreshCookieName 刷新令牌所在的 Cookie 名。
	refreshCookieName = "refresh_token"
	// refreshCookiePath 限制 Cookie 只在认证相关路径下发，缩小暴露面。
	refreshCookiePath = "/api/v1/auth"
)

// AuthHandler 是身份认证接口的网关处理器，经 gRPC 客户端访问用户服务的 AuthService。
//
// 刷新令牌由用户服务签发，网关只负责写入 HttpOnly Cookie；
// 退出登录是无状态 JWT 语义，仅清 Cookie，不调用任何 RPC。
type AuthHandler struct {
	client userv1.AuthServiceClient
	// secure 为 true 时 Cookie 仅通过 HTTPS 传输（生产环境）。
	secure bool
}

// NewAuthHandler 构造 AuthHandler。
func NewAuthHandler(client userv1.AuthServiceClient, secure bool) *AuthHandler {
	return &AuthHandler{client: client, secure: secure}
}

// SendCode 发送邮箱验证码：POST /api/v1/auth/email/code
//
// purpose 决定验证码用途（register / reset），校验与分发都在用户服务完成；
// 网关只做 HTTP ↔ gRPC 翻译，请求绑定复用 dto.SendCodeRequest。
func (h *AuthHandler) SendCode(c *gin.Context) {
	var req dto.SendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, err.Error())
		return
	}

	resp, err := h.client.SendCode(c.Request.Context(), &userv1.SendCodeRequest{
		Email:   req.Email,
		Ip:      c.ClientIP(),
		Purpose: req.Purpose,
	})
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	response.OK(c, dto.SendCodeResult{
		ExpiresIn: resp.GetExpiresIn(),
		Cooldown:  resp.GetCooldown(),
	})
}

// Register 处理用户注册：POST /api/v1/auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, err.Error())
		return
	}

	resp, err := h.client.Register(c.Request.Context(), &userv1.RegisterRequest{
		Email:    req.Email,
		Code:     req.Code,
		Password: req.Password,
		Nickname: req.Nickname,
	})
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	response.OK(c, toUserInfoDTO(resp.GetUser()))
}

// Login 处理用户登录：POST /api/v1/auth/login
// 成功后把刷新令牌写入 HttpOnly Cookie，访问令牌放在响应体。
func (h *AuthHandler) Login(c *gin.Context) {
	var req dto.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, err.Error())
		return
	}

	resp, err := h.client.Login(c.Request.Context(), &userv1.LoginRequest{
		Account:  req.Account,
		Password: req.Password,
	})
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	h.setRefreshCookie(c, resp.GetRefreshToken(), int(resp.GetRefreshExpiresIn()))
	response.OK(c, dto.TokenPair{
		AccessToken: resp.GetAccessToken(),
		TokenType:   resp.GetTokenType(),
		ExpiresIn:   resp.GetExpiresIn(),
	})
}

// Refresh 用刷新令牌换取新的访问令牌：POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(c *gin.Context) {
	refreshToken, err := c.Cookie(refreshCookieName)
	if err != nil || refreshToken == "" {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	resp, err := h.client.Refresh(c.Request.Context(), &userv1.RefreshRequest{RefreshToken: refreshToken})
	if err != nil {
		// 刷新失败说明令牌已失效，顺手清掉无效 Cookie，避免前端反复重试。
		h.clearRefreshCookie(c)
		failFromGRPCUser(c, err)
		return
	}
	h.setRefreshCookie(c, resp.GetRefreshToken(), int(resp.GetRefreshExpiresIn()))
	response.OK(c, dto.TokenPair{
		AccessToken: resp.GetAccessToken(),
		TokenType:   resp.GetTokenType(),
		ExpiresIn:   resp.GetExpiresIn(),
	})
}

// Logout 退出登录：POST /api/v1/auth/logout
// 当前版本采用无状态 JWT，退出即清除刷新令牌 Cookie（不调用任何 RPC）。
func (h *AuthHandler) Logout(c *gin.Context) {
	h.clearRefreshCookie(c)
	response.OK(c, nil)
}

// ResetPassword 用邮箱验证码重置密码：POST /api/v1/auth/password/reset
//
// 匿名接口：用户此刻正是登不进来才走这条路，不能要求任何身份。
// 成功后服务端已让该用户此前的刷新令牌全部失效，浏览器里那枚 Cookie
// 随之作废，顺手清掉，免得前端拿死 Cookie 反复打 /auth/refresh。
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var req dto.ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, err.Error())
		return
	}

	if _, err := h.client.ResetPassword(c.Request.Context(), &userv1.ResetPasswordRequest{
		Email:       req.Email,
		Code:        req.Code,
		NewPassword: req.NewPassword,
	}); err != nil {
		failFromGRPCUser(c, err)
		return
	}

	h.clearRefreshCookie(c)
	response.OK(c, nil)
}

// setRefreshCookie 写入刷新令牌 Cookie，Max-Age 取服务端回传的刷新令牌有效期。
func (h *AuthHandler) setRefreshCookie(c *gin.Context, token string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(
		refreshCookieName,
		token,
		maxAge,
		refreshCookiePath,
		"",
		h.secure,
		true, // HttpOnly：禁止 JS 读取，防 XSS 窃取
	)
}

// clearRefreshCookie 清除刷新令牌 Cookie。
func (h *AuthHandler) clearRefreshCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(refreshCookieName, "", -1, refreshCookiePath, "", h.secure, true)
}
