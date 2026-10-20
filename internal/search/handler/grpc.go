// Package handler 暴露 search 域的 gRPC 接口。
package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	searchv1 "github.com/sobremesatime/chuangyue/api/gen/search/v1"
	"github.com/sobremesatime/chuangyue/internal/search/service"
)

// GRPCSearchServer 是 search 域 gRPC 契约的服务端实现。
//
// 内嵌 Unimplemented 以满足前向兼容，未实现的方法会返回 Unimplemented。
type GRPCSearchServer struct {
	searchv1.UnimplementedSearchServiceServer
	svc *service.Service
}

// NewGRPCSearchServer 构造 GRPCSearchServer。
func NewGRPCSearchServer(svc *service.Service) *GRPCSearchServer {
	return &GRPCSearchServer{svc: svc}
}

// SearchVideos 按关键词检索视频，可按分类过滤。
//
// keyword / category_id / page / page_size 原样透传给 service；空关键词、未指定分类
// 与非法分页都不在这里报错，交给下游归一——检索接口对参数宽容，避免前端的小错误直接变成 500。
func (s *GRPCSearchServer) SearchVideos(ctx context.Context, req *searchv1.SearchVideosRequest) (*searchv1.SearchVideosResponse, error) {
	videos, total, err := s.svc.Search(ctx, req.GetKeyword(), req.GetCategoryId(), int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "检索视频失败: %v", err)
	}

	resp := &searchv1.SearchVideosResponse{Total: total}
	// service 返回的是值切片，这里取地址转成响应所需的指针切片。
	for i := range videos {
		resp.Videos = append(resp.Videos, &videos[i])
	}
	return resp, nil
}
