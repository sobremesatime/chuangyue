package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	interactiondto "github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// NotificationHandler 是站内通知能力的网关处理器，经 gRPC 客户端访问互动服务。
type NotificationHandler struct {
	client interactionv1.NotificationServiceClient
}

// NewNotificationHandler 构造 NotificationHandler。
func NewNotificationHandler(client interactionv1.NotificationServiceClient) *NotificationHandler {
	return &NotificationHandler{client: client}
}

// List 分页查询当前用户收到的通知：GET /api/v1/users/me/notifications
//
// 需要登录，按时间倒序。
func (h *NotificationHandler) List(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := h.client.ListNotifications(c.Request.Context(), &interactionv1.ListNotificationsRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}

	list := make([]interactiondto.NotificationInfo, 0, len(resp.GetNotifications()))
	for _, n := range resp.GetNotifications() {
		list = append(list, toNotificationInfoDTO(n))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// Unread 查询未读通知数：GET /api/v1/users/me/notifications/unread
//
// 需要登录，供顶栏红点轮询。
func (h *NotificationHandler) Unread(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	resp, err := h.client.GetUnreadCount(c.Request.Context(), &interactionv1.GetUnreadCountRequest{})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.NotificationUnread{UnreadCount: resp.GetUnreadCount()})
}

// MarkRead 标记已读：POST /api/v1/users/me/notifications/read
//
// 请求体 {"id": "123"} 标记单条；请求体为空（或 id 为 0）表示全部标记已读。
// 返回标记后的最新未读数。需要登录。
func (h *NotificationHandler) MarkRead(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	var in interactiondto.MarkNotificationsReadInput
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&in); err != nil {
			response.FailWithMsg(c, errcode.ErrInvalidParam, "请求体格式错误")
			return
		}
	}

	resp, err := h.client.MarkNotificationsRead(c.Request.Context(), &interactionv1.MarkNotificationsReadRequest{
		Id: in.ID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, interactiondto.NotificationUnread{UnreadCount: resp.GetUnreadCount()})
}
