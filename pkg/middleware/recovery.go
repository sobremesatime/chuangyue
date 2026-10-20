package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// Recovery 捕获 handler 中的 panic，记录堆栈并返回统一的 500 响应，
// 避免单个请求崩溃导致整个进程退出。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				logger.L().Error("请求发生 panic",
					zap.Any("panic", r),
					zap.String("path", c.Request.URL.Path),
					zap.String(response.TraceIDKey, c.GetString(response.TraceIDKey)),
					zap.ByteString("stack", debug.Stack()),
				)

				c.AbortWithStatusJSON(http.StatusInternalServerError, response.Body{
					Code:    errcode.ErrInternal.Code,
					Message: errcode.ErrInternal.Message,
					TraceID: c.GetString(response.TraceIDKey),
				})
			}
		}()
		c.Next()
	}
}
