// Package handler 暴露 feed 域的 gRPC 接口。
package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	feedv1 "github.com/sobremesatime/chuangyue/api/gen/feed/v1"
	"github.com/sobremesatime/chuangyue/internal/feed/service"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// GRPCFeedServer 是 feed 域 gRPC 契约的服务端实现。
//
// 内嵌 Unimplemented 以满足前向兼容，未实现的方法会返回 Unimplemented。
type GRPCFeedServer struct {
	feedv1.UnimplementedFeedServiceServer
	svc *service.Service
}

// NewGRPCFeedServer 构造 GRPCFeedServer。
func NewGRPCFeedServer(svc *service.Service) *GRPCFeedServer {
	return &GRPCFeedServer{svc: svc}
}

// GetHomeFeed 返回首页推荐流。
//
// 调用者身份来自 gRPC metadata 透传，未登录为 0（按热度回退），是合法值；
// page / page_size 原样透传给 service；page_size <= 0 或过大等非法参数
// 不在这里报错，交给 service 归一：首页接口对参数宽容，避免前端的小错误直接变成 500。
func (s *GRPCFeedServer) GetHomeFeed(ctx context.Context, req *feedv1.GetHomeFeedRequest) (*feedv1.GetHomeFeedResponse, error) {
	videos, total, err := s.svc.Home(ctx, identity.UserID(ctx), int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "获取首页推荐失败: %v", err)
	}

	resp := &feedv1.GetHomeFeedResponse{Total: total}
	// service 返回的是值切片，这里取地址转成响应所需的指针切片。
	for i := range videos {
		resp.Videos = append(resp.Videos, &videos[i])
	}
	return resp, nil
}
