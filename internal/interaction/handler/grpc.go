// Package handler 暴露互动域的 gRPC 接口。
package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
)

// GRPCInteractionServer 是互动域 gRPC 契约的服务端实现。
//
// 同一个 proto service 只能注册一个 server 实例，因此统计与用户信号两个
// service 由本结构一并持有。内嵌 Unimplemented 以满足前向兼容，未实现的方法返回 Unimplemented。
type GRPCInteractionServer struct {
	interactionv1.UnimplementedInteractionServiceServer
	stats   *service.StatsService
	signals *service.SignalService
}

// NewGRPCInteractionServer 构造 GRPCInteractionServer。
func NewGRPCInteractionServer(stats *service.StatsService, signals *service.SignalService) *GRPCInteractionServer {
	return &GRPCInteractionServer{stats: stats, signals: signals}
}

// GetVideoStats 批量返回视频的互动计数，完成 model 到 proto 的映射。
func (s *GRPCInteractionServer) GetVideoStats(ctx context.Context, req *interactionv1.GetVideoStatsRequest) (*interactionv1.GetVideoStatsResponse, error) {
	stats, err := s.stats.GetVideoStats(ctx, req.GetVideoIds())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "查询互动计数失败: %v", err)
	}

	resp := &interactionv1.GetVideoStatsResponse{
		Stats: make([]*interactionv1.VideoStats, 0, len(stats)),
	}
	for _, st := range stats {
		resp.Stats = append(resp.Stats, &interactionv1.VideoStats{
			VideoId:       st.VideoID,
			LikeCount:     st.LikeCount,
			FavoriteCount: st.FavoriteCount,
			CommentCount:  st.CommentCount,
		})
	}
	return resp, nil
}

// GetUserSignals 返回首页推荐所需的用户行为信号。
//
// 只做映射，limit 的默认值处理在 service 层；userID 非法时返回空信号而不报错，
// 首页对未登录 / 脏参数宽容，避免前端小错直接 500。
func (s *GRPCInteractionServer) GetUserSignals(ctx context.Context, req *interactionv1.GetUserSignalsRequest) (*interactionv1.GetUserSignalsResponse, error) {
	watched, engaged, err := s.signals.GetUserSignals(ctx, req.GetUserId(), int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "查询用户行为信号失败: %v", err)
	}
	return &interactionv1.GetUserSignalsResponse{
		WatchedVideoIds: watched,
		EngagedVideoIds: engaged,
	}, nil
}
