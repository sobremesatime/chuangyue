// Package router 负责组装 HTTP 路由：注册全局中间件、静态资源与各业务域接口。
//
// 这里是「依赖注入」的唯一入口：仓储 -> 服务 -> 处理器 的装配都发生在此处，
// 各层内部不再 new 依赖，便于测试时替换实现。
package router

import (
	"os"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	usermodel "github.com/sobremesatime/chuangyue/internal/user/model"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/jwt"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/ratelimit"
	"github.com/sobremesatime/chuangyue/pkg/response"

	feedv1 "github.com/sobremesatime/chuangyue/api/gen/feed/v1"
	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	searchv1 "github.com/sobremesatime/chuangyue/api/gen/search/v1"
	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	gatewayhandler "github.com/sobremesatime/chuangyue/internal/gateway/handler"
)

// Limiters 是网关限流所需的两个维度；任一为 nil 表示该维度不参与判定
// （限流未启用，或 Redis 不可用时的 fail-open）。
type Limiters struct {
	IP   *ratelimit.Limiter
	User *ratelimit.Limiter
}

// New 构造并返回配置完毕的 gin 引擎。
//
// authClient / userClient 是用户服务两个 gRPC 契约的客户端：用户域已全量切到
// 服务间调用，网关不再持有用户库、邮件发送器与令牌签发，只保留 JWT 验签（中间件需要）；
// Redis 仅用于限流（缓存与验证码都在各自的服务里）。
//
// videoClient / uploadClient / reportClient 是视频服务三个 gRPC 契约的客户端：
// 视频域已全量切到服务间调用，网关不再持有视频库与视频存储，只做 HTTP ↔ gRPC 翻译。
//
// interactionClient / commentClient / danmakuClient / notificationClient 是互动服务四个
// gRPC 契约的客户端：互动域已全量切到服务间调用，网关不再持有互动库与 MySQL，
// 互动表（点赞 / 收藏 / 评论 / 弹幕 / 通知）的唯一后端是 cmd/interaction。
//
// feedClient 是推荐服务的 gRPC 契约客户端：首页推荐流经它读取（网关不参与推荐计算）。
//
// searchClient 是搜索服务的 gRPC 契约客户端：视频检索经它读取（搜索服务自身再经视频服务检索）。
func New(cfg *config.Config, limits Limiters, authClient userv1.AuthServiceClient, userClient userv1.UserServiceClient, videoClient videov1.VideoServiceClient, uploadClient videov1.UploadServiceClient, reportClient videov1.ReportServiceClient, interactionClient interactionv1.UserInteractionServiceClient, commentClient interactionv1.CommentServiceClient, danmakuClient interactionv1.DanmakuServiceClient, notificationClient interactionv1.NotificationServiceClient, feedClient feedv1.FeedServiceClient, searchClient searchv1.SearchServiceClient) *gin.Engine {
	gin.SetMode(ginMode(cfg.App.Mode))

	r := gin.New()

	// 可信反向代理：决定 c.ClientIP() 如何取值，直接影响按 IP 的限流是否可信。
	//
	// 必须显式设置：gin 的默认值是「信任所有代理」，会直接采信客户端可伪造的
	// X-Forwarded-For，一个假请求头就能绕过按 IP 的限流。
	// 而传空列表（本项目默认）等于「不信任任何代理」：ClientIP 取直连地址，
	// 伪造的 X-Forwarded-For 被忽略——安全，但若前面有 Nginx 会把所有请求
	// 算成同一个来源，此时应配置成 Nginx 的地址（见 config.AppConfig.TrustedProxies）。
	if err := r.SetTrustedProxies(cfg.App.TrustedProxies); err != nil {
		logger.L().Warn("可信代理配置非法，回退为「不信任任何代理」",
			zap.Strings("trusted_proxies", cfg.App.TrustedProxies), zap.Error(err))
		_ = r.SetTrustedProxies(nil)
	}

	// 顺序很重要：指标在最外层（统计到的是用户视角的完整耗时），
	// 然后 TraceID（后续日志/响应都依赖它），再 Recovery 兜底 panic，最后访问日志与跨域。
	r.Use(
		metrics.Default().GinMiddleware(),
		middleware.TraceID(),
		middleware.Recovery(),
		middleware.AccessLog(),
		middleware.CORS(corsOrigins(cfg)),
	)

	// 健康检查，供 k8s 探针 / 负载均衡使用。
	r.GET("/healthz", func(c *gin.Context) {
		response.OK(c, gin.H{"status": "ok"})
	})

	// Prometheus 抓取端点。
	//
	// 刻意挂在 /api/v1 之外：指标是运维接口，不该被业务限流拖累（否则限流一触发，
	// 恰恰是最需要看指标的时候抓不到数据）。生产环境应在接入层（Nginx / Ingress）
	// 限制它只对监控网段开放。
	r.GET("/metrics", gin.WrapH(metrics.Default().Handler()))

	// pprof 性能剖析：与 /metrics 一样属于运维接口，刻意挂在 /api/v1 与限流之外。
	//
	// 网关的 /metrics 走 gin 路由、不经过 metrics.StartServer，因而 pprof 也需在此单独挂载；
	// 整棵 /debug/pprof 子树在一个 mux 上登记好后整体代理，/debug/pprof/heap 等命名
	// profile 由 pprof.Index 按路径自行分发。
	//
	// 安全提示：生产环境应在接入层（Nginx / Ingress）限制该路径只对监控网段开放——
	// /profile、/trace 会消耗可观 CPU，暴露即等于给出一个可被利用的放大点。
	r.Any("/debug/pprof/*any", gin.WrapH(metrics.PprofHandler()))

	// 本地静态资源：当前版本上传的视频与封面直接由网关托管。
	// 后续版本会切到 MinIO / CDN，这里仅保留开发期便利。
	registerStatic(r, cfg)

	jwtMgr := jwt.NewManager(cfg.JWT)
	secureCookie := cfg.App.Env == "prod"

	// 用户域已全量切到 gRPC：认证与资料接口都由网关翻译成对用户服务的调用，
	// 网关不再持有用户库 / 验证码 / 邮件 / 令牌签发。
	authHandler := gatewayhandler.NewAuthHandler(authClient, secureCookie)
	userHandler := gatewayhandler.NewUserHandler(userClient)
	// 用户管理是管理员对任意账号的操作，与「本人资料」分开：路由挂 /admin/users。
	userAdminHandler := gatewayhandler.NewUserAdminHandler(userClient)
	// 视频域已全量切到 gRPC：读路径、管理端与上传均由网关翻译成对视频服务的调用，
	// 网关不再持有视频仓储与存储。
	videoReadHandler := gatewayhandler.NewVideoReadHandler(videoClient)
	videoAdminHandler := gatewayhandler.NewVideoAdminHandler(videoClient)
	uploadHandler := gatewayhandler.NewUploadHandler(uploadClient)
	// 互动域已全量切到 gRPC：评论 / 弹幕 / 视频互动 / 通知都由网关翻译成对互动服务的调用，
	// 网关不再持有互动仓储与 MySQL，用户展示信息由互动服务内部经 user gRPC 补全。
	interactionHandler := gatewayhandler.NewInteractionHandler(interactionClient)
	commentHandler := gatewayhandler.NewCommentHandler(commentClient)
	danmakuHandler := gatewayhandler.NewDanmakuHandler(danmakuClient)
	notificationHandler := gatewayhandler.NewNotificationHandler(notificationClient)
	reportHandler := gatewayhandler.NewReportHandler(reportClient)
	// 推荐域：首页推荐流由独立的推荐服务计算，网关只做 HTTP ↔ gRPC 翻译。
	feedHandler := gatewayhandler.NewFeedHandler(feedClient)
	// 搜索域：检索由独立的搜索服务承担，网关只做 HTTP ↔ gRPC 翻译。
	searchHandler := gatewayhandler.NewSearchHandler(searchClient)

	// IP 维度限流挂在 /api/v1 分组上：所有接口先过一遍（含公开的读接口——
	// 详情页会写播放量，正是脚本刷量最容易打热的路径）。
	api := r.Group("/api/v1", middleware.RateLimit(limits.IP, nil))

	// 用户维度依赖登录身份，必须排在 Auth 之后，因此用 authed() 组合到需要登录的路由上。
	// 为什么要两个维度：同一 NAT 下多个用户共享一个出口 IP 时，只有 IP 维度会互相牵连。
	userLimit := middleware.RateLimit(nil, limits.User)
	authed := func(h gin.HandlerFunc) []gin.HandlerFunc {
		return []gin.HandlerFunc{middleware.Auth(jwtMgr), userLimit, h}
	}

	{
		auth := api.Group("/auth")
		{
			auth.POST("/email/code", authHandler.SendCode)
			auth.POST("/register", authHandler.Register)
			auth.POST("/login", authHandler.Login)
			auth.POST("/refresh", authHandler.Refresh)
			auth.POST("/logout", authed(authHandler.Logout)...)
			// 忘记密码：发码走 /email/code（purpose=reset），重置走这里。
			// 必须匿名——用户正是登不进来才走这条路。
			auth.POST("/password/reset", authHandler.ResetPassword)
		}

		// 需要登录的普通用户接口。
		api.GET("/users/me", authed(userHandler.Profile)...)
		api.POST("/users/me/avatar", authed(userHandler.UploadAvatar)...)
		api.PATCH("/users/me", authed(userHandler.UpdateProfile)...)
		api.PUT("/users/me/password", authed(userHandler.ChangePassword)...)

		// 视频浏览：无需登录，经视频服务 gRPC 读取。
		api.GET("/videos", videoReadHandler.List)
		api.GET("/videos/:id", videoReadHandler.Detail)

		// 首页推荐流：公开（未登录按全局热度，登录按个性化），经推荐服务 gRPC 读取。
		api.GET("/feed", middleware.AuthOptional(jwtMgr), feedHandler.Home)

		// 视频检索：公开，经搜索服务 gRPC 读取（搜索服务自身再经视频服务检索）。
		api.GET("/search/videos", searchHandler.Videos)

		// 视频评论：列表公开（登录后附带自己的点赞态），发表、删除与点赞需要登录。
		api.GET("/videos/:id/comments", middleware.AuthOptional(jwtMgr), commentHandler.List)
		api.POST("/videos/:id/comments", authed(commentHandler.Create)...)
		// 一级评论下的回复列表：同样公开，登录后附带自己的点赞态。
		api.GET("/comments/:id/replies", middleware.AuthOptional(jwtMgr), commentHandler.Replies)
		api.DELETE("/comments/:id", authed(commentHandler.Delete)...)
		api.POST("/comments/:id/like", authed(commentHandler.Like)...)
		api.DELETE("/comments/:id/like", authed(commentHandler.Unlike)...)

		// 视频弹幕：列表公开，发表与删除需要登录。
		api.GET("/videos/:id/danmaku", danmakuHandler.List)
		api.POST("/videos/:id/danmaku", authed(danmakuHandler.Create)...)
		api.DELETE("/danmaku/:id", authed(danmakuHandler.Delete)...)

		// 内容举报：发起举报需要登录，管理端在下方 admin 分组处理。
		api.POST("/reports", authed(reportHandler.Create)...)

		// 用户互动：点赞、收藏、稍后再看、观看历史，均需登录。
		api.GET("/videos/:id/interaction", authed(interactionHandler.State)...)
		api.POST("/videos/:id/like", authed(interactionHandler.Like)...)
		api.DELETE("/videos/:id/like", authed(interactionHandler.Unlike)...)
		api.POST("/videos/:id/favorite", authed(interactionHandler.Favorite)...)
		api.DELETE("/videos/:id/favorite", authed(interactionHandler.Unfavorite)...)
		api.POST("/videos/:id/watch-later", authed(interactionHandler.WatchLater)...)
		api.DELETE("/videos/:id/watch-later", authed(interactionHandler.UnwatchLater)...)
		api.POST("/videos/:id/history", authed(interactionHandler.ReportHistory)...)

		// 「我」分组：登录用户自己的点赞 / 收藏 / 稍后再看 / 观看历史列表。
		api.GET("/users/me/likes", authed(interactionHandler.MyLikes)...)
		api.GET("/users/me/favorites", authed(interactionHandler.MyFavorites)...)
		api.GET("/users/me/watch-later", authed(interactionHandler.MyWatchLater)...)
		api.GET("/users/me/history", authed(interactionHandler.MyHistory)...)

		// 站内通知：列表、未读数、标记已读，均需登录。
		api.GET("/users/me/notifications", authed(notificationHandler.List)...)
		api.GET("/users/me/notifications/unread", authed(notificationHandler.Unread)...)
		api.POST("/users/me/notifications/read", authed(notificationHandler.MarkRead)...)

		// 管理员接口：登录 + 角色校验双重保护。限流同样排在 Auth 之后（按管理员账号计数）。
		admin := api.Group("/admin",
			middleware.Auth(jwtMgr),
			userLimit,
			middleware.RequireRole(usermodel.RoleAdmin),
		)
		{
			// 用户管理：检索账号、封禁 / 解封、调整角色。
			admin.GET("/users", userAdminHandler.AdminList)
			admin.PATCH("/users/:id", userAdminHandler.AdminUpdate)

			admin.POST("/videos", uploadHandler.Upload)
			admin.GET("/videos", videoAdminHandler.AdminList)
			admin.PATCH("/videos/:id", videoAdminHandler.UpdateStatus)
			// 转码失败重试：复用已上传的源文件，不再要求管理员重传。
			admin.POST("/videos/:id/transcode", videoAdminHandler.RetryTranscode)
			admin.PUT("/videos/:id", videoAdminHandler.Update)
			admin.DELETE("/videos/:id", videoAdminHandler.Delete)

			// 内容治理：评论 / 弹幕的检索与删除，以及举报的处理。
			admin.GET("/comments", commentHandler.AdminList)
			admin.DELETE("/comments/:id", commentHandler.AdminDelete)
			admin.GET("/danmaku", danmakuHandler.AdminList)
			admin.DELETE("/danmaku/:id", danmakuHandler.AdminDelete)
			admin.GET("/reports", reportHandler.AdminList)
			admin.PATCH("/reports/:id", reportHandler.Handle)

			// 分片上传 / 断点续传：初始化、查询进度、分片预签名与直传、完成与中止。
			// 初始化放在 /uploads 上，避免与 /uploads/:id 通配在同段冲突。
			admin.POST("/uploads", uploadHandler.InitUpload)
			admin.GET("/uploads/:id", uploadHandler.UploadStatus)
			admin.POST("/uploads/:id/parts/:n/presign", uploadHandler.PresignPart)
			admin.PUT("/uploads/:id/parts/:n", uploadHandler.PutPart)
			admin.POST("/uploads/:id/complete", uploadHandler.CompleteUpload)
			admin.DELETE("/uploads/:id", uploadHandler.AbortUpload)
		}
	}

	return r
}

// registerStatic 挂载上传目录为静态资源。
func registerStatic(r *gin.Engine, cfg *config.Config) {
	if err := os.MkdirAll(cfg.Upload.LocalDir, 0o755); err != nil {
		logger.L().Warn("创建上传目录失败", zap.String("dir", cfg.Upload.LocalDir), zap.Error(err))
		return
	}
	r.Static("/uploads", cfg.Upload.LocalDir)
}

// corsOrigins 返回允许跨域的来源。
//
// 生产环境由 Nginx 同源代理，无需跨域；开发环境放行 Vite 默认端口。
func corsOrigins(cfg *config.Config) []string {
	if cfg.App.Env == "prod" {
		return nil
	}
	return []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
	}
}

// ginMode 把配置中的运行模式映射为 gin 的模式常量。
func ginMode(mode string) string {
	if mode == "release" {
		return gin.ReleaseMode
	}
	return gin.DebugMode
}
