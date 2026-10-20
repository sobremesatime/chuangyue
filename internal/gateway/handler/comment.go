package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	interactiondto "github.com/sobremesatime/chuangyue/internal/interaction/dto"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// CommentHandler 是视频评论能力的网关处理器，经 gRPC 客户端访问互动服务。
type CommentHandler struct {
	client interactionv1.CommentServiceClient
}

// NewCommentHandler 构造 CommentHandler。
func NewCommentHandler(client interactionv1.CommentServiceClient) *CommentHandler {
	return &CommentHandler{client: client}
}

// List 分页查询某视频的评论：GET /api/v1/videos/{id}/comments
//
// 公开接口，不需要登录；已登录时额外标记每条评论是否被当前用户点赞。
func (h *CommentHandler) List(c *gin.Context) {
	videoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || videoID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := h.client.ListComments(c.Request.Context(), &interactionv1.ListCommentsRequest{
		VideoId:  videoID,
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}

	list := make([]interactiondto.CommentInfo, 0, len(resp.GetComments()))
	for _, cm := range resp.GetComments() {
		list = append(list, toCommentInfoDTO(cm))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// Create 发表评论：POST /api/v1/videos/{id}/comments
//
// 请求体 {"content": "...", "parent_id": "..."}，parent_id 可选：
// 传了表示回复该评论（后端统一扁平化到对应的一级评论下）。需要登录。
func (h *CommentHandler) Create(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	videoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || videoID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	var in interactiondto.CreateCommentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "请求体格式错误")
		return
	}

	resp, err := h.client.CreateComment(c.Request.Context(), &interactionv1.CreateCommentRequest{
		VideoId:  videoID,
		ParentId: in.ParentID,
		Content:  in.Content,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, toCommentInfoDTO(resp.GetComment()))
}

// Replies 分页查询某条一级评论下的回复：GET /api/v1/comments/{id}/replies
//
// 公开接口，不需要登录；已登录时额外标记每条回复是否被当前用户点赞。
func (h *CommentHandler) Replies(c *gin.Context) {
	parentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || parentID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的评论 ID")
		return
	}

	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := h.client.ListReplies(c.Request.Context(), &interactionv1.ListRepliesRequest{
		ParentId: parentID,
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}

	list := make([]interactiondto.CommentInfo, 0, len(resp.GetComments()))
	for _, cm := range resp.GetComments() {
		list = append(list, toCommentInfoDTO(cm))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// Delete 删除评论：DELETE /api/v1/comments/{id}
//
// 仅作者本人或管理员可删，需要登录。
func (h *CommentHandler) Delete(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	commentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || commentID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的评论 ID")
		return
	}

	if _, err := h.client.DeleteComment(c.Request.Context(), &interactionv1.DeleteCommentRequest{
		CommentId: commentID,
	}); err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, nil)
}

// Like 点赞评论：POST /api/v1/comments/{id}/like
//
// 需要登录。
func (h *CommentHandler) Like(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	commentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || commentID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的评论 ID")
		return
	}

	resp, err := h.client.LikeComment(c.Request.Context(), &interactionv1.LikeCommentRequest{
		CommentId: commentID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.CommentLikeResult{Liked: resp.GetLiked(), LikeCount: resp.GetLikeCount()})
}

// Unlike 取消点赞评论：DELETE /api/v1/comments/{id}/like
//
// 需要登录。
func (h *CommentHandler) Unlike(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	commentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || commentID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的评论 ID")
		return
	}

	resp, err := h.client.UnlikeComment(c.Request.Context(), &interactionv1.UnlikeCommentRequest{
		CommentId: commentID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.CommentLikeResult{Liked: resp.GetLiked(), LikeCount: resp.GetLikeCount()})
}

// AdminList 分页查询全部评论，供管理端内容治理：GET /api/v1/admin/comments
//
// keyword 查询参数可选，按评论内容模糊匹配。
func (h *CommentHandler) AdminList(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := h.client.AdminListComments(c.Request.Context(), &interactionv1.AdminListCommentsRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
		Keyword:  c.Query("keyword"),
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}

	list := make([]videodto.AdminCommentInfo, 0, len(resp.GetComments()))
	for _, cm := range resp.GetComments() {
		list = append(list, toAdminCommentInfoDTO(cm))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// AdminDelete 管理端删除评论：DELETE /api/v1/admin/comments/{id}
//
// 采用软删保留记录。需要登录且为管理员（由路由的 RequireRole 保证）。
func (h *CommentHandler) AdminDelete(c *gin.Context) {
	commentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || commentID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的评论 ID")
		return
	}

	resp, err := h.client.AdminDeleteComment(c.Request.Context(), &interactionv1.AdminDeleteCommentRequest{
		CommentId: commentID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	// 雪花 ID 以字符串回传，与列表、详情保持一致
	response.OK(c, gin.H{"id": strconv.FormatInt(resp.GetId(), 10)})
}
