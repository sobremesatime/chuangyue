package handler

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	interactiondto "github.com/sobremesatime/chuangyue/internal/interaction/dto"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// 本文件同时承载互动域网关 handler 共享的错误映射与出参映射：
// 评论 / 弹幕 / 通知三个 handler 与本文件的 InteractionHandler 都在同一包内复用它们。

// InteractionHandler 是用户与视频互动能力（点赞 / 收藏 / 稍后再看 / 观看历史 / 「我」列表）
// 的网关处理器，经 gRPC 客户端访问互动服务。
type InteractionHandler struct {
	client interactionv1.UserInteractionServiceClient
}

// NewInteractionHandler 构造 InteractionHandler。
func NewInteractionHandler(client interactionv1.UserInteractionServiceClient) *InteractionHandler {
	return &InteractionHandler{client: client}
}

// Like 点赞视频：POST /api/v1/videos/{id}/like
func (h *InteractionHandler) Like(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}
	resp, err := h.client.LikeVideo(c.Request.Context(), &interactionv1.LikeVideoRequest{
		VideoId: videoID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.LikeResult{Liked: resp.GetLiked(), LikeCount: resp.GetLikeCount()})
}

// Unlike 取消点赞：DELETE /api/v1/videos/{id}/like
func (h *InteractionHandler) Unlike(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}
	resp, err := h.client.UnlikeVideo(c.Request.Context(), &interactionv1.UnlikeVideoRequest{
		VideoId: videoID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.LikeResult{Liked: resp.GetLiked(), LikeCount: resp.GetLikeCount()})
}

// Favorite 收藏视频：POST /api/v1/videos/{id}/favorite
func (h *InteractionHandler) Favorite(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}
	resp, err := h.client.FavoriteVideo(c.Request.Context(), &interactionv1.FavoriteVideoRequest{
		VideoId: videoID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.FavoriteResult{Favorited: resp.GetFavorited(), FavoriteCount: resp.GetFavoriteCount()})
}

// Unfavorite 取消收藏：DELETE /api/v1/videos/{id}/favorite
func (h *InteractionHandler) Unfavorite(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}
	resp, err := h.client.UnfavoriteVideo(c.Request.Context(), &interactionv1.UnfavoriteVideoRequest{
		VideoId: videoID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.FavoriteResult{Favorited: resp.GetFavorited(), FavoriteCount: resp.GetFavoriteCount()})
}

// WatchLater 加入稍后再看：POST /api/v1/videos/{id}/watch-later
func (h *InteractionHandler) WatchLater(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}
	resp, err := h.client.AddWatchLater(c.Request.Context(), &interactionv1.AddWatchLaterRequest{
		VideoId: videoID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.WatchLaterResult{WatchLater: resp.GetWatchLater()})
}

// UnwatchLater 移出稍后再看：DELETE /api/v1/videos/{id}/watch-later
func (h *InteractionHandler) UnwatchLater(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}
	resp, err := h.client.RemoveWatchLater(c.Request.Context(), &interactionv1.RemoveWatchLaterRequest{
		VideoId: videoID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.WatchLaterResult{WatchLater: resp.GetWatchLater()})
}

// State 查询当前用户对该视频的互动状态：GET /api/v1/videos/{id}/interaction
func (h *InteractionHandler) State(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}
	resp, err := h.client.GetInteractionState(c.Request.Context(), &interactionv1.GetInteractionStateRequest{
		VideoId: videoID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.InteractionState{
		Liked:      resp.GetLiked(),
		Favorited:  resp.GetFavorited(),
		WatchLater: resp.GetWatchLater(),
		Progress:   int(resp.GetProgress()),
	})
}

// ReportHistory 上报观看历史：POST /api/v1/videos/{id}/history
//
// 请求体 {"progress": 123}；允许空请求体，按进度 0 处理。
func (h *InteractionHandler) ReportHistory(c *gin.Context) {
	videoID, ok := h.actorAndVideo(c)
	if !ok {
		return
	}

	var in interactiondto.HistoryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		in.Progress = 0
	}

	if _, err := h.client.ReportHistory(c.Request.Context(), &interactionv1.ReportHistoryRequest{
		VideoId:  videoID,
		Progress: int32(in.Progress),
	}); err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, nil)
}

// MyLikes 我赞过的视频：GET /api/v1/users/me/likes
func (h *InteractionHandler) MyLikes(c *gin.Context) {
	h.listMine(c, h.client.ListLiked)
}

// MyFavorites 我的收藏：GET /api/v1/users/me/favorites
func (h *InteractionHandler) MyFavorites(c *gin.Context) {
	h.listMine(c, h.client.ListFavorites)
}

// MyHistory 观看历史：GET /api/v1/users/me/history
func (h *InteractionHandler) MyHistory(c *gin.Context) {
	h.listMine(c, h.client.ListHistory)
}

// MyWatchLater 稍后再看：GET /api/v1/users/me/watch-later
func (h *InteractionHandler) MyWatchLater(c *gin.Context) {
	h.listMine(c, h.client.ListWatchLater)
}

// mineFetcher 是「我」列表共用的查询函数签名，四个 ListMine 系 RPC 签名一致。
type mineFetcher func(
	ctx context.Context, in *interactionv1.ListMineRequest, opts ...grpc.CallOption,
) (*interactionv1.VideoCardListResponse, error)

// listMine 收敛「我」列表接口的分页解析与响应编排。
func (h *InteractionHandler) listMine(c *gin.Context, fetch mineFetcher) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := fetch(c.Request.Context(), &interactionv1.ListMineRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}

	list := make([]videodto.VideoInfo, 0, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		list = append(list, toVideoCardDTO(v))
	}
	// 分页沿用服务层归一后的值，网关不重复归一，避免二次归一造成口径漂移。
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// actorAndVideo 校验登录并解析路径中的视频 ID。
func (h *InteractionHandler) actorAndVideo(c *gin.Context) (int64, bool) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return 0, false
	}

	videoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || videoID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return 0, false
	}
	return videoID, true
}

// failFromInteractionGRPC 把互动服务的 gRPC 错误映射为对外响应。
//
// 必须先 Unwrap：服务端把业务错误码编进 status.message 原样还原，才能保证 HTTP 状态码
// 与响应 code 与改造前逐字一致。尤其是「评论 / 弹幕不存在」（30006 / 30007）——
// 它们并非 NotFound 兜底里的 404，而是 HTTP 200 + 对应 code，若先兜底会破坏口径。
// 未命中（如传输层故障）交给 FailErr 统一记录日志并返回 500。
func failFromInteractionGRPC(c *gin.Context, err error) {
	if ec, ok := grpcerr.Unwrap(err); ok {
		response.FailWithMsg(c, ec, ec.Message)
		return
	}
	response.FailErr(c, err)
}

// toCommentInfoDTO 把跨服务契约的评论信息映射回对外 DTO，字段与序列化口径均保持不变。
func toCommentInfoDTO(p *interactionv1.CommentInfo) interactiondto.CommentInfo {
	info := interactiondto.CommentInfo{
		ID:              p.GetId(),
		VideoID:         p.GetVideoId(),
		UserID:          p.GetUserId(),
		Username:        p.GetUsername(),
		Nickname:        p.GetNickname(),
		Avatar:          p.GetAvatar(),
		Content:         p.GetContent(),
		LikeCount:       p.GetLikeCount(),
		Liked:           p.GetLiked(),
		ParentID:        p.GetParentId(),
		ReplyToUserID:   p.GetReplyToUserId(),
		ReplyToNickname: p.GetReplyToNickname(),
		ReplyCount:      p.GetReplyCount(),
		// 恒为非 nil 空切片：proto 空 repeated 直接映射会得到 nil，序列化成 null 与现状 [] 不符。
		Replies: make([]interactiondto.CommentInfo, 0, len(p.GetReplies())),
	}
	// created_at 经 gRPC 传为 UTC 时间戳，转回本地时区，序列化结果与进程内直读数据库一致。
	if ts := p.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	for _, r := range p.GetReplies() {
		info.Replies = append(info.Replies, toCommentInfoDTO(r))
	}
	return info
}

// toAdminCommentInfoDTO 把跨服务契约的管理端评论条目映射回对外 DTO。
func toAdminCommentInfoDTO(p *interactionv1.AdminCommentInfo) videodto.AdminCommentInfo {
	info := videodto.AdminCommentInfo{
		ID:              p.GetId(),
		VideoID:         p.GetVideoId(),
		VideoTitle:      p.GetVideoTitle(),
		UserID:          p.GetUserId(),
		Nickname:        p.GetNickname(),
		Avatar:          p.GetAvatar(),
		Content:         p.GetContent(),
		ParentID:        p.GetParentId(),
		ReplyToNickname: p.GetReplyToNickname(),
		ReplyCount:      p.GetReplyCount(),
		LikeCount:       p.GetLikeCount(),
	}
	if ts := p.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}

// toDanmakuInfoDTO 把跨服务契约的弹幕信息映射回对外 DTO（mode int32 → int8）。
func toDanmakuInfoDTO(p *interactionv1.DanmakuInfo) interactiondto.DanmakuInfo {
	info := interactiondto.DanmakuInfo{
		ID:       p.GetId(),
		VideoID:  p.GetVideoId(),
		UserID:   p.GetUserId(),
		Username: p.GetUsername(),
		Nickname: p.GetNickname(),
		Avatar:   p.GetAvatar(),
		Content:  p.GetContent(),
		TimeMs:   int(p.GetTimeMs()),
		Color:    p.GetColor(),
		Mode:     int8(p.GetMode()),
	}
	if ts := p.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}

// toAdminDanmakuInfoDTO 把跨服务契约的管理端弹幕条目映射回对外 DTO（mode int32 → int8）。
func toAdminDanmakuInfoDTO(p *interactionv1.AdminDanmakuInfo) videodto.AdminDanmakuInfo {
	info := videodto.AdminDanmakuInfo{
		ID:         p.GetId(),
		VideoID:    p.GetVideoId(),
		VideoTitle: p.GetVideoTitle(),
		UserID:     p.GetUserId(),
		Nickname:   p.GetNickname(),
		Content:    p.GetContent(),
		TimeMs:     int(p.GetTimeMs()),
		Mode:       int8(p.GetMode()),
	}
	if ts := p.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}

// toNotificationInfoDTO 把跨服务契约的通知映射回对外 DTO（type int32 → int8）。
func toNotificationInfoDTO(p *interactionv1.NotificationInfo) interactiondto.NotificationInfo {
	info := interactiondto.NotificationInfo{
		ID:         p.GetId(),
		Type:       int8(p.GetType()),
		ActorID:    p.GetActorId(),
		Nickname:   p.GetNickname(),
		Avatar:     p.GetAvatar(),
		VideoID:    p.GetVideoId(),
		VideoTitle: p.GetVideoTitle(),
		Content:    p.GetContent(),
		IsRead:     p.GetIsRead(),
	}
	if ts := p.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}

// toVideoCardDTO 把跨服务契约的互动列表视频卡片映射回视频域对外 DTO。
func toVideoCardDTO(p *interactionv1.VideoCard) videodto.VideoInfo {
	info := videodto.VideoInfo{
		ID:            p.GetId(),
		Title:         p.GetTitle(),
		Description:   p.GetDescription(),
		CoverURL:      p.GetCoverUrl(),
		PlayURL:       p.GetPlayUrl(),
		Duration:      int(p.GetDuration()),
		CategoryID:    p.GetCategoryId(),
		ViewCount:     p.GetViewCount(),
		LikeCount:     p.GetLikeCount(),
		FavoriteCount: p.GetFavoriteCount(),
		Status:        int8(p.GetStatus()),
		ErrorMsg:      p.GetErrorMsg(),
	}
	if ts := p.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}
