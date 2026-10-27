package handler

import (
	"context"
	"strconv"

	"google.golang.org/protobuf/types/known/timestamppb"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// ============================================================
// VideoService：合集（跨视频的播放列表）
// ============================================================

// GetCollection 返回公开合集详情与其中已上架的视频。
//
// 草稿合集与不存在一样返回「资源不存在」：公开读不该暴露草稿的存在。
func (s *GRPCVideoServer) GetCollection(ctx context.Context, req *videov1.GetCollectionRequest) (*videov1.GetCollectionResponse, error) {
	detail, err := s.svc.GetCollection(ctx, req.GetId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	resp := &videov1.GetCollectionResponse{
		Collection: toCollectionInfoProto(&detail.CollectionInfo),
		Videos:     make([]*videov1.VideoInfo, 0, len(detail.Videos)),
	}
	for i := range detail.Videos {
		resp.Videos = append(resp.Videos, toVideoInfoFromDTO(&detail.Videos[i]))
	}
	return resp, nil
}

// AdminGetCollection 返回任意状态合集的详情与全部视频，供管理端编排排序使用。
//
// 与 GetCollection 的差别：草稿合集照常返回，未上架（含转码中）的视频也不过滤，
// 否则管理端拿到的顺序与库内不一致，重排必然被判「集合不一致」。
func (s *GRPCVideoServer) AdminGetCollection(ctx context.Context, req *videov1.AdminGetCollectionRequest) (*videov1.AdminGetCollectionResponse, error) {
	detail, err := s.svc.AdminGetCollection(ctx, req.GetId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	resp := &videov1.AdminGetCollectionResponse{
		Collection: toCollectionInfoProto(&detail.CollectionInfo),
		Videos:     make([]*videov1.VideoInfo, 0, len(detail.Videos)),
	}
	for i := range detail.Videos {
		resp.Videos = append(resp.Videos, toVideoInfoFromDTO(&detail.Videos[i]))
	}
	return resp, nil
}

// GetCollectionByVideo 反查视频所属的公开合集，并给出该视频在合集内的 1 基序号。
func (s *GRPCVideoServer) GetCollectionByVideo(ctx context.Context, req *videov1.GetCollectionByVideoRequest) (*videov1.GetCollectionByVideoResponse, error) {
	briefs, err := s.svc.GetCollectionByVideo(ctx, req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	resp := &videov1.GetCollectionByVideoResponse{
		Collections: make([]*videov1.CollectionBrief, 0, len(briefs)),
	}
	for i := range briefs {
		resp.Collections = append(resp.Collections, toCollectionBriefProto(&briefs[i]))
	}
	return resp, nil
}

// ListCollections 分页返回公开合集，供网关的 GET /collections 使用。
func (s *GRPCVideoServer) ListCollections(ctx context.Context, req *videov1.ListCollectionsRequest) (*videov1.ListCollectionsResponse, error) {
	list, total, page, pageSize, err := s.svc.ListCollections(ctx, int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.ListCollectionsResponse{
		Collections: toCollectionInfoProtos(list),
		Total:       total,
		Page:        int32(page),
		PageSize:    int32(pageSize),
	}, nil
}

// AdminListCollections 分页返回全部合集（含草稿），供管理后台列表使用。
func (s *GRPCVideoServer) AdminListCollections(ctx context.Context, req *videov1.AdminListCollectionsRequest) (*videov1.AdminListCollectionsResponse, error) {
	list, total, page, pageSize, err := s.svc.AdminListCollections(ctx, int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.AdminListCollectionsResponse{
		Collections: toCollectionInfoProtos(list),
		Total:       total,
		Page:        int32(page),
		PageSize:    int32(pageSize),
	}, nil
}

// CreateCollection 新建合集。
//
// 创建者身份来自 gRPC metadata 透传（与 UploadVideo 相同），未登录直接拒绝。
func (s *GRPCVideoServer) CreateCollection(ctx context.Context, req *videov1.CreateCollectionRequest) (*videov1.CreateCollectionResponse, error) {
	uploaderID, err := collectionActor(ctx)
	if err != nil {
		return nil, err
	}

	info, err := s.svc.CreateCollection(ctx, uploaderID, dto.CreateCollectionInput{
		Title:       req.GetTitle(),
		Description: req.GetDescription(),
		CoverURL:    req.GetCoverUrl(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.CreateCollectionResponse{Collection: toCollectionInfoProto(info)}, nil
}

// UpdateCollection 编辑合集；optional 字段未传表示不修改。
func (s *GRPCVideoServer) UpdateCollection(ctx context.Context, req *videov1.UpdateCollectionRequest) (*videov1.UpdateCollectionResponse, error) {
	if _, err := collectionActor(ctx); err != nil {
		return nil, err
	}

	in := dto.UpdateCollectionInput{
		Title:       req.Title,
		Description: req.Description,
		CoverURL:    req.CoverUrl,
	}
	if req.Status != nil {
		status := int8(*req.Status)
		in.Status = &status
	}

	info, err := s.svc.UpdateCollection(ctx, req.GetId(), in)
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.UpdateCollectionResponse{Collection: toCollectionInfoProto(info)}, nil
}

// DeleteCollection 删除合集及其全部关联行；成功时回传字符串形式的 ID。
func (s *GRPCVideoServer) DeleteCollection(ctx context.Context, req *videov1.DeleteCollectionRequest) (*videov1.DeleteCollectionResponse, error) {
	if _, err := collectionActor(ctx); err != nil {
		return nil, err
	}
	if err := s.svc.DeleteCollection(ctx, req.GetId()); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.DeleteCollectionResponse{Id: strconv.FormatInt(req.GetId(), 10)}, nil
}

// AddCollectionVideos 按传入顺序把视频追加到合集末尾（重复添加忽略）。
func (s *GRPCVideoServer) AddCollectionVideos(ctx context.Context, req *videov1.AddCollectionVideosRequest) (*videov1.AddCollectionVideosResponse, error) {
	if _, err := collectionActor(ctx); err != nil {
		return nil, err
	}
	info, err := s.svc.AddCollectionVideos(ctx, req.GetCollectionId(), req.GetVideoIds())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.AddCollectionVideosResponse{Collection: toCollectionInfoProto(info)}, nil
}

// RemoveCollectionVideo 从合集移出一个视频（幂等）。
func (s *GRPCVideoServer) RemoveCollectionVideo(ctx context.Context, req *videov1.RemoveCollectionVideoRequest) (*videov1.RemoveCollectionVideoResponse, error) {
	if _, err := collectionActor(ctx); err != nil {
		return nil, err
	}
	info, err := s.svc.RemoveCollectionVideo(ctx, req.GetCollectionId(), req.GetVideoId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.RemoveCollectionVideoResponse{Collection: toCollectionInfoProto(info)}, nil
}

// ReorderCollectionVideos 按传入的完整顺序重写播放顺序。
func (s *GRPCVideoServer) ReorderCollectionVideos(ctx context.Context, req *videov1.ReorderCollectionVideosRequest) (*videov1.ReorderCollectionVideosResponse, error) {
	if _, err := collectionActor(ctx); err != nil {
		return nil, err
	}
	info, err := s.svc.ReorderCollectionVideos(ctx, req.GetCollectionId(), req.GetVideoIds())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.ReorderCollectionVideosResponse{Collection: toCollectionInfoProto(info)}, nil
}

// collectionActor 取出合集写操作的调用者身份，未登录直接拒绝。
//
// 合集写操作都挂在网关的管理端分组下，角色校验已由网关完成；
// 这里只兜底「身份确实随 metadata 透传到了」，与 UploadVideo 的判据一致。
func collectionActor(ctx context.Context) (int64, error) {
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return 0, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	return userID, nil
}

// toCollectionInfoProto 把合集 DTO 映射为契约结构。
func toCollectionInfoProto(c *dto.CollectionInfo) *videov1.CollectionInfo {
	return &videov1.CollectionInfo{
		Id:          c.ID,
		Title:       c.Title,
		Description: c.Description,
		CoverUrl:    c.CoverURL,
		UploaderId:  c.UploaderID,
		Status:      int32(c.Status),
		VideoCount:  c.VideoCount,
		CreatedAt:   timestamppb.New(c.CreatedAt),
	}
}

// toCollectionInfoProtos 批量映射合集；空切片返回空切片而非 nil，保持 JSON 为 []。
func toCollectionInfoProtos(list []dto.CollectionInfo) []*videov1.CollectionInfo {
	out := make([]*videov1.CollectionInfo, 0, len(list))
	for i := range list {
		out = append(out, toCollectionInfoProto(&list[i]))
	}
	return out
}

// toCollectionBriefProto 把「视频所属合集」DTO 映射为契约结构。
func toCollectionBriefProto(b *dto.CollectionBrief) *videov1.CollectionBrief {
	return &videov1.CollectionBrief{
		Id:           b.ID,
		Title:        b.Title,
		CoverUrl:     b.CoverURL,
		VideoCount:   b.VideoCount,
		CurrentIndex: int32(b.CurrentIndex),
	}
}
