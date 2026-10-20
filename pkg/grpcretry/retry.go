// Package grpcretry 为「幂等的跨服务读调用」提供统一的自动重试。
//
// 为什么必须用白名单：重试是否安全完全取决于方法是否幂等——读方法重复执行没有副作用，
// 写方法重试则可能重复下单、重复计数、重复发通知。因此这里**显式列出**允许重试的方法，
// 未列出的默认不重试（保守优先）。新增 RPC 时不要顺手加进来，先确认「重复执行与执行一次等价」。
//
// 为什么只重试 codes.Unavailable：它是最典型的瞬时故障（下游进程重启、实例从 Etcd 摘除、
// 连接被拒），重试往往立刻就好；其余错误要么是调用方的错（参数 / 权限 / 资源不存在），
// 要么重试也没用（服务端内部错误），重试只会浪费调用方的时间预算。
package grpcretry

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	feedv1 "github.com/sobremesatime/chuangyue/api/gen/feed/v1"
	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	searchv1 "github.com/sobremesatime/chuangyue/api/gen/search/v1"
	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
)

const (
	// maxAttempts 是含首次在内的最大尝试次数。
	maxAttempts = 3
	// initialBackoff / maxBackoff 是退避等待的起点与上限（每次失败翻倍）。
	initialBackoff = 100 * time.Millisecond
	maxBackoff     = 500 * time.Millisecond
)

// retryable 是允许自动重试的方法白名单，只含幂等的读方法。
//
// 用生成代码里的 *_FullMethodName 常量而不是手写字符串：方法改名时这里会编译失败，
// 而不是悄悄退化成「不再重试」。
var retryable = map[string]struct{}{
	// 用户域：资料查询与批量用户信息（供互动域补全昵称头像）
	userv1.UserService_GetProfile_FullMethodName: {},
	userv1.UserService_GetUsers_FullMethodName:   {},

	// 视频域：读路径与转码状态查询
	videov1.VideoService_GetVideo_FullMethodName:           {},
	videov1.VideoService_GetVideos_FullMethodName:          {},
	videov1.VideoService_GetVideoInfos_FullMethodName:      {},
	videov1.VideoService_ListVideos_FullMethodName:         {},
	videov1.VideoService_ListFeedCandidates_FullMethodName: {},
	videov1.VideoService_AdminListVideos_FullMethodName:    {},
	videov1.VideoService_GetTranscodeState_FullMethodName:  {},
	videov1.ReportService_ListReports_FullMethodName:       {},

	// 互动域：计数 / 信号 / 评论 / 弹幕 / 「我」列表 / 通知的读路径
	interactionv1.InteractionService_GetVideoStats_FullMethodName:           {},
	interactionv1.InteractionService_GetUserSignals_FullMethodName:          {},
	interactionv1.CommentService_ListComments_FullMethodName:                {},
	interactionv1.CommentService_ListReplies_FullMethodName:                 {},
	interactionv1.CommentService_AdminListComments_FullMethodName:           {},
	interactionv1.DanmakuService_ListDanmaku_FullMethodName:                 {},
	interactionv1.DanmakuService_AdminListDanmaku_FullMethodName:            {},
	interactionv1.UserInteractionService_GetInteractionState_FullMethodName: {},
	interactionv1.UserInteractionService_ListLiked_FullMethodName:           {},
	interactionv1.UserInteractionService_ListFavorites_FullMethodName:       {},
	interactionv1.UserInteractionService_ListHistory_FullMethodName:         {},
	interactionv1.UserInteractionService_ListWatchLater_FullMethodName:      {},
	interactionv1.NotificationService_ListNotifications_FullMethodName:      {},
	interactionv1.NotificationService_GetUnreadCount_FullMethodName:         {},

	// 推荐与搜索：纯读
	feedv1.FeedService_GetHomeFeed_FullMethodName:      {},
	searchv1.SearchService_SearchVideos_FullMethodName: {},
}

// UnaryClientInterceptor 对白名单内的读方法自动重试瞬时故障。
//
// 只拦截一元调用：流式调用是上传 / 头像这类长连接，重放成本高且大多非幂等，不在此列。
// 退避等待会响应 context 取消，因此上游设的超时会同时约束「本次调用」与「重试总耗时」。
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if _, ok := retryable[method]; !ok {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		backoff := initialBackoff
		var lastErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if lastErr = invoker(ctx, method, req, reply, cc, opts...); lastErr == nil {
				return nil
			}
			// 非瞬时故障：立刻返回，不浪费预算。
			if status.Code(lastErr) != codes.Unavailable {
				return lastErr
			}
			// 已是最后一次，或调用方已超时 / 取消：不再等待。
			if attempt == maxAttempts || ctx.Err() != nil {
				return lastErr
			}

			// 重试前清空上一轮的响应：protobuf 解组是合并语义，不清空会把两次结果叠在一起。
			if m, ok := reply.(proto.Message); ok {
				proto.Reset(m)
			}

			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return lastErr
			case <-timer.C:
			}

			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		return lastErr
	}
}
