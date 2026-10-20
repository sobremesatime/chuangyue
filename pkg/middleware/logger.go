package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// AccessLog 记录每个请求的访问日志：方法、路径、状态码、耗时、客户端 IP、链路 ID。
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		logger.L().Info("http 请求",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("client_ip", c.ClientIP()),
			zap.String(response.TraceIDKey, c.GetString(response.TraceIDKey)),
		)
	}
}
