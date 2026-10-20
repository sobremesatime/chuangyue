package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS 处理跨域请求。
//
// 生产环境由 Nginx 同源代理，通常无需跨域；本中间件主要服务于本地开发
// （前端 Vite 运行在 http://localhost:5173，后端在 :8080）。
//
// allowedOrigins 传入允许的来源列表；包含 "*" 时放行全部来源，
// 但此时浏览器不允许携带 Cookie，刷新令牌将无法工作，故仅建议纯开放接口使用。
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := false
	whitelist := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o == "*" {
			allowAll = true
		}
		whitelist[o] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if allowAll {
				c.Header("Access-Control-Allow-Origin", "*")
			} else if _, ok := whitelist[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				// 允许携带 Cookie（刷新令牌放在 HttpOnly Cookie 中）。
				c.Header("Access-Control-Allow-Credentials", "true")
				c.Header("Vary", "Origin")
			}
		}

		c.Header("Access-Control-Allow-Methods", strings.Join([]string{
			http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
			http.MethodDelete, http.MethodOptions,
		}, ", "))
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Request-ID")
		c.Header("Access-Control-Expose-Headers", HeaderRequestID)
		c.Header("Access-Control-Max-Age", "86400")

		// 预检请求直接返回，不进入业务处理链。
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
