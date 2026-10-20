// Package handler 暴露互动域「用户与视频互动」能力的 gRPC 服务端实现。
package handler

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// GRPCUserInteractionServer 是用户与视频互动 gRPC 契约的服务端实现。
type GRPCUserInteractionServer struct {
	interactionv1.UnimplementedUserInteractionServiceServer
	interaction *service.InteractionService
}

// NewGRPCUserInteractionServer 构造 GRPCUserInteractionServer。
func NewGRPCUserInteractionServer(interaction *service.InteractionService) *GRPCUserInteractionServer {
	return &GRPCUserInteractionServer{interaction: interaction}
}

// LikeVideo 点赞视频。
func (s *GRPCUserInteractionServer) LikeVideo(ctx context.Context, req *interactionv1.LikeVideoRequest) (*interactionv1.LikeResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.interaction.Like(ctx, userID, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.LikeResult{Liked: result.Liked, LikeCount: result.LikeCount}, nil
}

// UnlikeVideo 取消点赞。
func (s *GRPCUserInteractionServer) UnlikeVideo(ctx context.Context, req *interactionv1.UnlikeVideoRequest) (*interactionv1.LikeResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.interaction.Unlike(ctx, userID, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.LikeResult{Liked: result.Liked, LikeCount: result.LikeCount}, nil
}

// FavoriteVideo 收藏视频。
func (s *GRPCUserInteractionServer) FavoriteVideo(ctx context.Context, req *interactionv1.FavoriteVideoRequest) (*interactionv1.FavoriteResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.interaction.Favorite(ctx, userID, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.FavoriteResult{Favorited: result.Favorited, FavoriteCount: result.FavoriteCount}, nil
}

// UnfavoriteVideo 取消收藏。
func (s *GRPCUserInteractionServer) UnfavoriteVideo(ctx context.Context, req *interactionv1.UnfavoriteVideoRequest) (*interactionv1.FavoriteResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.interaction.Unfavorite(ctx, userID, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.FavoriteResult{Favorited: result.Favorited, FavoriteCount: result.FavoriteCount}, nil
}

// AddWatchLater 加入稍后再看。
func (s *GRPCUserInteractionServer) AddWatchLater(ctx context.Context, req *interactionv1.AddWatchLaterRequest) (*interactionv1.WatchLaterResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.interaction.WatchLater(ctx, userID, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.WatchLaterResult{WatchLater: result.WatchLater}, nil
}

// RemoveWatchLater 移出稍后再看。
func (s *GRPCUserInteractionServer) RemoveWatchLater(ctx context.Context, req *interactionv1.RemoveWatchLaterRequest) (*interactionv1.WatchLaterResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.interaction.UnwatchLater(ctx, userID, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.WatchLaterResult{WatchLater: result.WatchLater}, nil
}

// GetInteractionState 查询当前用户对该视频的互动状态。
func (s *GRPCUserInteractionServer) GetInteractionState(ctx context.Context, req *interactionv1.GetInteractionStateRequest) (*interactionv1.InteractionState, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	state, err := s.interaction.State(ctx, userID, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.InteractionState{
		Liked:      state.Liked,
		Favorited:  state.Favorited,
		WatchLater: state.WatchLater,
		Progress:   int32(state.Progress),
	}, nil
}

// ReportHistory 上报观看历史。
func (s *GRPCUserInteractionServer) ReportHistory(ctx context.Context, req *interactionv1.ReportHistoryRequest) (*interactionv1.ReportHistoryResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	if err := s.interaction.ReportHistory(ctx, userID, req.GetVideoId(), int(req.GetProgress())); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.ReportHistoryResponse{}, nil
}

// ListLiked 分页查询用户点赞过的视频。
func (s *GRPCUserInteractionServer) ListLiked(ctx context.Context, req *interactionv1.ListMineRequest) (*interactionv1.VideoCardListResponse, error) {
	// 「我」列表的身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	list, total, p, ps, err := s.interaction.ListLiked(ctx, userID, int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return toVideoCardListResponse(list, total, p, ps), nil
}

// ListFavorites 分页查询用户收藏的视频。
func (s *GRPCUserInteractionServer) ListFavorites(ctx context.Context, req *interactionv1.ListMineRequest) (*interactionv1.VideoCardListResponse, error) {
	// 「我」列表的身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	list, total, p, ps, err := s.interaction.ListFavorites(ctx, userID, int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return toVideoCardListResponse(list, total, p, ps), nil
}

// ListHistory 分页查询用户的观看历史。
func (s *GRPCUserInteractionServer) ListHistory(ctx context.Context, req *interactionv1.ListMineRequest) (*interactionv1.VideoCardListResponse, error) {
	// 「我」列表的身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	list, total, p, ps, err := s.interaction.ListHistory(ctx, userID, int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return toVideoCardListResponse(list, total, p, ps), nil
}

// ListWatchLater 分页查询用户稍后再看的视频。
func (s *GRPCUserInteractionServer) ListWatchLater(ctx context.Context, req *interactionv1.ListMineRequest) (*interactionv1.VideoCardListResponse, error) {
	// 「我」列表的身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	list, total, p, ps, err := s.interaction.ListWatchLater(ctx, userID, int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return toVideoCardListResponse(list, total, p, ps), nil
}

// toVideoCardListResponse 把视频信息列表与分页信息打包成统一响应。
func toVideoCardListResponse(list []videodto.VideoInfo, total int64, p, ps int) *interactionv1.VideoCardListResponse {
	videos := make([]*interactionv1.VideoCard, 0, len(list))
	for i := range list {
		videos = append(videos, toProtoVideoCard(&list[i]))
	}
	return &interactionv1.VideoCardListResponse{
		Videos:   videos,
		Total:    total,
		Page:     int32(p),
		PageSize: int32(ps),
	}
}

// toProtoVideoCard 把视频 DTO 映射为互动列表里的视频卡片。
func toProtoVideoCard(v *videodto.VideoInfo) *interactionv1.VideoCard {
	card := &interactionv1.VideoCard{
		Id:            v.ID,
		Title:         v.Title,
		Description:   v.Description,
		CoverUrl:      v.CoverURL,
		PlayUrl:       v.PlayURL,
		Duration:      int32(v.Duration),
		CategoryId:    v.CategoryID,
		ViewCount:     v.ViewCount,
		LikeCount:     v.LikeCount,
		FavoriteCount: v.FavoriteCount,
		Status:        int32(v.Status),
		ErrorMsg:      v.ErrorMsg,
	}
	if !v.CreatedAt.IsZero() {
		card.CreatedAt = timestamppb.New(v.CreatedAt)
	}
	return card
}
