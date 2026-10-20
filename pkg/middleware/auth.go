package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/identity"
	"github.com/sobremesatime/chuangyue/pkg/jwt"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// Auth 校验访问令牌，并把用户 ID 与角色写入上下文。
//
// 令牌来源：请求头 Authorization: Bearer <token>。
// 校验失败统一返回 401，前端据此触发刷新令牌流程。
func Auth(m *jwt.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearer(c.GetHeader("Authorization"))
		if token == "" {
			response.Abort(c, errcode.ErrUnauthorized)
			return
		}

		claims, err := m.ParseAccessToken(token)
		if err != nil {
			response.Abort(c, errcode.ErrTokenInvalid)
			return
		}

		setIdentity(c, claims.UserID, claims.Role)
		c.Next()
	}
}

// AuthOptional 尽最大努力解析访问令牌：带了且有效就写入上下文，否则直接放行。
//
// 供「公开接口 + 登录后附加个性化字段」的场景使用（例如评论列表带出当前用户的点赞态），
// 与 Auth 的区别是这里不会因缺少或无效令牌而中断请求。
func AuthOptional(m *jwt.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearer(c.GetHeader("Authorization"))
		if token != "" {
			if claims, err := m.ParseAccessToken(token); err == nil {
				setIdentity(c, claims.UserID, claims.Role)
			}
		}
		c.Next()
	}
}

// setIdentity 写入已认证身份，同时落到两处：
//   - gin 上下文：供本进程的 handler 用 middleware.UserID / Role 读取；
//   - 请求 context：供下游 gRPC 调用的客户端拦截器放进 metadata（见 pkg/identity）。
func setIdentity(c *gin.Context, userID int64, role int8) {
	c.Set(ContextUserID, userID)
	c.Set(ContextRole, role)
	c.Request = c.Request.WithContext(
		identity.WithUser(c.Request.Context(), identity.User{ID: userID, Role: role}),
	)
}

// RequireRole 要求当前用户角色属于 roles 之一，否则返回 403。
// 需与 Auth 中间件配合使用。
func RequireRole(roles ...int8) gin.HandlerFunc {
	allowed := make(map[int8]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(c *gin.Context) {
		role, ok := Role(c)
		if !ok {
			response.Abort(c, errcode.ErrUnauthorized)
			return
		}
		if _, ok := allowed[role]; !ok {
			response.Abort(c, errcode.ErrForbidden)
			return
		}
		c.Next()
	}
}

// UserID 从上下文读取当前登录用户 ID。
func UserID(c *gin.Context) (int64, bool) {
	v, exists := c.Get(ContextUserID)
	if !exists {
		return 0, false
	}
	id, ok := v.(int64)
	return id, ok
}

// Role 从上下文读取当前登录用户角色。
func Role(c *gin.Context) (int8, bool) {
	v, exists := c.Get(ContextRole)
	if !exists {
		return 0, false
	}
	role, ok := v.(int8)
	return role, ok
}

// extractBearer 从 Authorization 头中解析出 Bearer 令牌。
func extractBearer(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}
