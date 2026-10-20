// Package handler 暴露互动域站内通知能力的 gRPC 服务端实现。
package handler

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	"github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// GRPCNotificationServer 是站内通知 gRPC 契约的服务端实现。
type GRPCNotificationServer struct {
	interactionv1.UnimplementedNotificationServiceServer
	notifications *service.NotificationService
}

// NewGRPCNotificationServer 构造 GRPCNotificationServer。
func NewGRPCNotificationServer(notifications *service.NotificationService) *GRPCNotificationServer {
	return &GRPCNotificationServer{notifications: notifications}
}

// ListNotifications 分页查询当前用户收到的通知。
func (s *GRPCNotificationServer) ListNotifications(ctx context.Context, req *interactionv1.ListNotificationsRequest) (*interactionv1.ListNotificationsResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	list, total, p, ps, err := s.notifications.List(ctx, userID, int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	items := make([]*interactionv1.NotificationInfo, 0, len(list))
	for i := range list {
		items = append(items, toProtoNotificationInfo(&list[i]))
	}
	return &interactionv1.ListNotificationsResponse{
		Notifications: items,
		Total:         total,
		Page:          int32(p),
		PageSize:      int32(ps),
	}, nil
}

// GetUnreadCount 查询未读通知数。
func (s *GRPCNotificationServer) GetUnreadCount(ctx context.Context, req *interactionv1.GetUnreadCountRequest) (*interactionv1.NotificationUnread, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.notifications.Unread(ctx, userID)
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.NotificationUnread{UnreadCount: result.UnreadCount}, nil
}

// MarkNotificationsRead 标记已读（id=0 表示全部），返回最新未读数。
func (s *GRPCNotificationServer) MarkNotificationsRead(ctx context.Context, req *interactionv1.MarkNotificationsReadRequest) (*interactionv1.NotificationUnread, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.notifications.MarkRead(ctx, userID, req.GetId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.NotificationUnread{UnreadCount: result.UnreadCount}, nil
}

// toProtoNotificationInfo 把通知 DTO 映射为跨服务契约结构。
func toProtoNotificationInfo(n *dto.NotificationInfo) *interactionv1.NotificationInfo {
	info := &interactionv1.NotificationInfo{
		Id:         n.ID,
		Type:       int32(n.Type),
		ActorId:    n.ActorID,
		Nickname:   n.Nickname,
		Avatar:     n.Avatar,
		VideoId:    n.VideoID,
		VideoTitle: n.VideoTitle,
		Content:    n.Content,
		IsRead:     n.IsRead,
	}
	if !n.CreatedAt.IsZero() {
		info.CreatedAt = timestamppb.New(n.CreatedAt)
	}
	return info
}
