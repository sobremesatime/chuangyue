package middleware

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/ratelimit"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// RateLimit 按 IP 与登录用户两个维度做令牌桶限流。
//
// 两个 limiter 任一为 nil 表示该维度不参与判定（限流未启用，或 Redis 不可用）。
//
// 失败策略是 fail-open：Redis 报错时放行并记警告。限流是保护措施而不是业务功能，
// 保护组件自身故障时不该把站点一起拒绝掉——那等于把「限流」变成了新的单点故障。
//
// 关于挂载位置：用户维度依赖登录身份，所以带用户维度的实例必须挂在 Auth **之后**；
// IP 维度不依赖身份，挂在分组上即可（见 internal/gateway/router 的装配）。
func RateLimit(ip, user *ratelimit.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		if ip != nil {
			if res, err := ip.Allow(ctx, c.ClientIP()); err != nil {
				logger.L().Warn("IP 限流判定失败，放行本次请求", logger.TraceField(ctx), zap.Error(err))
			} else if !res.Allowed {
				// 单独计数：限流拒绝返回 429，混在业务错误率里看不出「保护措施在起作用」。
				metrics.Default().IncRateLimited("ip")
				response.Abort(c, errcode.ErrTooManyReq)
				return
			}
		}

		if user != nil {
			if uid, ok := UserID(c); ok {
				if res, err := user.Allow(ctx, strconv.FormatInt(uid, 10)); err != nil {
					logger.L().Warn("用户限流判定失败，放行本次请求", logger.TraceField(ctx), zap.Error(err))
				} else if !res.Allowed {
					metrics.Default().IncRateLimited("user")
					response.Abort(c, errcode.ErrTooManyReq)
					return
				}
			}
		}

		c.Next()
	}
}
