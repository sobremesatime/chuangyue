// Package handler 暴露互动域弹幕能力的 gRPC 服务端实现。
package handler

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	"github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// GRPCDanmakuServer 是互动域弹幕 gRPC 契约的服务端实现。
type GRPCDanmakuServer struct {
	interactionv1.UnimplementedDanmakuServiceServer
	danmaku *service.DanmakuService
}

// NewGRPCDanmakuServer 构造 GRPCDanmakuServer。
func NewGRPCDanmakuServer(danmaku *service.DanmakuService) *GRPCDanmakuServer {
	return &GRPCDanmakuServer{danmaku: danmaku}
}

// ListDanmaku 拉取某视频下的全部正常弹幕。
func (s *GRPCDanmakuServer) ListDanmaku(ctx context.Context, req *interactionv1.ListDanmakuRequest) (*interactionv1.ListDanmakuResponse, error) {
	list, err := s.danmaku.List(ctx, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	items := make([]*interactionv1.DanmakuInfo, 0, len(list))
	for i := range list {
		items = append(items, toProtoDanmakuInfo(&list[i]))
	}
	return &interactionv1.ListDanmakuResponse{Danmaku: items}, nil
}

// CreateDanmaku 发表弹幕。
func (s *GRPCDanmakuServer) CreateDanmaku(ctx context.Context, req *interactionv1.CreateDanmakuRequest) (*interactionv1.CreateDanmakuResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	info, err := s.danmaku.Create(ctx, userID, req.GetVideoId(), dto.CreateDanmakuInput{
		Content: req.GetContent(),
		TimeMs:  int(req.GetTimeMs()),
		Color:   req.GetColor(),
		Mode:    int8(req.GetMode()),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.CreateDanmakuResponse{Danmaku: toProtoDanmakuInfo(info)}, nil
}

// DeleteDanmaku 删除弹幕（仅发送者本人或管理员）。
func (s *GRPCDanmakuServer) DeleteDanmaku(ctx context.Context, req *interactionv1.DeleteDanmakuRequest) (*interactionv1.DeleteDanmakuResponse, error) {
	// 操作者身份与角色来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	if err := s.danmaku.Delete(ctx, req.GetDanmakuId(), userID, identity.IsAdmin(ctx)); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.DeleteDanmakuResponse{}, nil
}

// AdminListDanmaku 分页查询全部正常弹幕，供管理端内容治理。
func (s *GRPCDanmakuServer) AdminListDanmaku(ctx context.Context, req *interactionv1.AdminListDanmakuRequest) (*interactionv1.AdminListDanmakuResponse, error) {
	list, total, p, ps, err := s.danmaku.AdminList(ctx, videodto.AdminContentQuery{
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
		Keyword:  req.GetKeyword(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	items := make([]*interactionv1.AdminDanmakuInfo, 0, len(list))
	for i := range list {
		items = append(items, toProtoAdminDanmakuInfo(&list[i]))
	}
	return &interactionv1.AdminListDanmakuResponse{
		Danmaku:  items,
		Total:    total,
		Page:     int32(p),
		PageSize: int32(ps),
	}, nil
}

// AdminDeleteDanmaku 管理端删除弹幕。
func (s *GRPCDanmakuServer) AdminDeleteDanmaku(ctx context.Context, req *interactionv1.AdminDeleteDanmakuRequest) (*interactionv1.AdminDeleteDanmakuResponse, error) {
	// 操作者身份来自 metadata：删完要按它给发送者写「内容被删除」通知（自己删自己不提醒）。
	operatorID := identity.UserID(ctx)
	if operatorID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	if err := s.danmaku.AdminDelete(ctx, operatorID, req.GetDanmakuId()); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.AdminDeleteDanmakuResponse{Id: req.GetDanmakuId()}, nil
}

// toProtoDanmakuInfo 把弹幕 DTO 映射为跨服务契约结构。
func toProtoDanmakuInfo(d *dto.DanmakuInfo) *interactionv1.DanmakuInfo {
	info := &interactionv1.DanmakuInfo{
		Id:       d.ID,
		VideoId:  d.VideoID,
		UserId:   d.UserID,
		Username: d.Username,
		Nickname: d.Nickname,
		Avatar:   d.Avatar,
		Content:  d.Content,
		TimeMs:   int32(d.TimeMs),
		Color:    d.Color,
		Mode:     int32(d.Mode),
	}
	if !d.CreatedAt.IsZero() {
		info.CreatedAt = timestamppb.New(d.CreatedAt)
	}
	return info
}

// toProtoAdminDanmakuInfo 把管理端弹幕 DTO 映射为跨服务契约结构。
func toProtoAdminDanmakuInfo(d *videodto.AdminDanmakuInfo) *interactionv1.AdminDanmakuInfo {
	info := &interactionv1.AdminDanmakuInfo{
		Id:         d.ID,
		VideoId:    d.VideoID,
		VideoTitle: d.VideoTitle,
		UserId:     d.UserID,
		Nickname:   d.Nickname,
		Content:    d.Content,
		TimeMs:     int32(d.TimeMs),
		Mode:       int32(d.Mode),
	}
	if !d.CreatedAt.IsZero() {
		info.CreatedAt = timestamppb.New(d.CreatedAt)
	}
	return info
}
