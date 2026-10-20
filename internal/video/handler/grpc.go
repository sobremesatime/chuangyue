// Package handler 暴露视频域的 gRPC 接口。
package handler

import (
	"context"
	"errors"
	"io"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// errFrameFormat 表示客户端在元信息帧之后又发来非数据帧，属协议错误。
var errFrameFormat = grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("数据帧格式错误"))

// ============================================================
// VideoService：读路径（原有）+ 管理端不含字节体的 CRUD
// ============================================================

// GRPCVideoServer 是视频域 gRPC 契约的服务端实现。
//
// 内嵌 Unimplemented 以满足前向兼容，未实现的方法会返回 Unimplemented。
type GRPCVideoServer struct {
	videov1.UnimplementedVideoServiceServer
	svc *service.Service
}

// NewGRPCVideoServer 构造 GRPCVideoServer。
//
// 依赖 *service.Service 即可覆盖整个视频域：Service 内嵌 QueryService，
// 读路径与写路径共用同一个仓储。
func NewGRPCVideoServer(svc *service.Service) *GRPCVideoServer {
	return &GRPCVideoServer{svc: svc}
}

// GetVideo 返回单个视频的最小信息；不存在时返回 NotFound。
func (s *GRPCVideoServer) GetVideo(ctx context.Context, req *videov1.GetVideoRequest) (*videov1.GetVideoResponse, error) {
	v, err := s.svc.GetByID(ctx, req.GetId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Errorf(codes.NotFound, "视频不存在: %d", req.GetId())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "查询视频失败: %v", err)
	}
	return &videov1.GetVideoResponse{Video: toVideoInfo(v)}, nil
}

// GetVideos 批量返回视频的最小信息。
func (s *GRPCVideoServer) GetVideos(ctx context.Context, req *videov1.GetVideosRequest) (*videov1.GetVideosResponse, error) {
	videos, err := s.svc.GetByIDs(ctx, req.GetIds())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "批量查询视频失败: %v", err)
	}

	resp := &videov1.GetVideosResponse{Videos: make([]*videov1.VideoBrief, 0, len(videos))}
	for i := range videos {
		resp.Videos = append(resp.Videos, toVideoBrief(&videos[i]))
	}
	return resp, nil
}

// GetVideoInfos 批量返回视频的完整信息，供互动域「我」列表与管理端治理列表使用。
//
// 与 GetVideos 的区别：返回 VideoInfo（含 description / play_url / error_msg），
// 让互动域不必按 id 逐个 GetVideo。缺失的 id 直接不出现在结果里，由调用方按 id 归位。
func (s *GRPCVideoServer) GetVideoInfos(ctx context.Context, req *videov1.GetVideoInfosRequest) (*videov1.GetVideoInfosResponse, error) {
	videos, err := s.svc.GetByIDs(ctx, req.GetIds())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "批量查询视频失败: %v", err)
	}

	resp := &videov1.GetVideoInfosResponse{Videos: make([]*videov1.VideoInfo, 0, len(videos))}
	for i := range videos {
		resp.Videos = append(resp.Videos, toVideoInfo(&videos[i]))
	}
	return resp, nil
}

// ListFeedCandidates 返回首页候选池：已上架视频按热度倒序取前 limit 条。
func (s *GRPCVideoServer) ListFeedCandidates(ctx context.Context, req *videov1.ListFeedCandidatesRequest) (*videov1.ListFeedCandidatesResponse, error) {
	videos, err := s.svc.ListFeedCandidates(ctx, int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "查询首页候选池失败: %v", err)
	}

	resp := &videov1.ListFeedCandidatesResponse{Videos: make([]*videov1.VideoBrief, 0, len(videos))}
	for i := range videos {
		resp.Videos = append(resp.Videos, toVideoBrief(&videos[i]))
	}
	return resp, nil
}

// ListVideos 分页返回已上架视频（支持分类过滤、关键词、排序），供网关的 GET /videos 使用。
func (s *GRPCVideoServer) ListVideos(ctx context.Context, req *videov1.ListVideosRequest) (*videov1.ListVideosResponse, error) {
	videos, total, err := s.svc.ListVideos(ctx, req.GetCategoryId(), req.GetKeyword(), req.GetSort(), int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "查询视频列表失败: %v", err)
	}

	resp := &videov1.ListVideosResponse{
		Videos: make([]*videov1.VideoInfo, 0, len(videos)),
		Total:  total,
	}
	for i := range videos {
		resp.Videos = append(resp.Videos, toVideoInfo(&videos[i]))
	}
	return resp, nil
}

// IncrViewCount 播放量自增。视频不存在时返回 NotFound，与 GetVideo 的语义保持一致。
func (s *GRPCVideoServer) IncrViewCount(ctx context.Context, req *videov1.IncrViewCountRequest) (*videov1.IncrViewCountResponse, error) {
	// 先确认视频存在：UPDATE 命中 0 行不算错误，必须靠一次读来区分「不存在」与「更新成功」。
	if _, err := s.svc.GetByID(ctx, req.GetVideoId()); errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Errorf(codes.NotFound, "视频不存在: %d", req.GetVideoId())
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "查询视频失败: %v", err)
	}
	if err := s.svc.IncrViewCount(ctx, req.GetVideoId()); err != nil {
		return nil, status.Errorf(codes.Internal, "播放量自增失败: %v", err)
	}
	return &videov1.IncrViewCountResponse{}, nil
}

// GetTranscodeState 返回转码 Worker 判定幂等所需的最小状态；不存在时返回 NotFound。
func (s *GRPCVideoServer) GetTranscodeState(ctx context.Context, req *videov1.GetTranscodeStateRequest) (*videov1.GetTranscodeStateResponse, error) {
	v, err := s.svc.GetByID(ctx, req.GetVideoId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Errorf(codes.NotFound, "视频不存在: %d", req.GetVideoId())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "查询视频状态失败: %v", err)
	}
	return &videov1.GetTranscodeStateResponse{
		VideoId:   v.ID,
		Status:    int32(v.Status),
		SourceUrl: v.SourceURL,
	}, nil
}

// ReportTranscodeResult 由转码 Worker 回写转码结果，返回空响应表示受理。
func (s *GRPCVideoServer) ReportTranscodeResult(ctx context.Context, req *videov1.ReportTranscodeResultRequest) (*videov1.ReportTranscodeResultResponse, error) {
	err := s.svc.ReportTranscodeResult(
		ctx, req.GetVideoId(), req.GetPlayUrl(), int(req.GetDuration()), int8(req.GetStatus()), req.GetErrorMsg(),
	)
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.ReportTranscodeResultResponse{}, nil
}

// AdminListVideos 分页查询全部视频（含未上架），供管理后台列表使用。
//
// status 用「是否为 nil」表达「未传」：0（转码中）是合法状态，用 0 兜底会与不筛选混淆。
func (s *GRPCVideoServer) AdminListVideos(ctx context.Context, req *videov1.AdminListVideosRequest) (*videov1.AdminListVideosResponse, error) {
	var status *int8
	if req.Status != nil {
		v := int8(*req.Status)
		status = &v
	}

	list, total, page, pageSize, err := s.svc.AdminList(ctx, dto.AdminListQuery{
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
		Status:   status,
		Keyword:  req.GetKeyword(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	resp := &videov1.AdminListVideosResponse{
		Videos:   make([]*videov1.VideoInfo, 0, len(list)),
		Total:    total,
		Page:     int32(page),
		PageSize: int32(pageSize),
	}
	for i := range list {
		resp.Videos = append(resp.Videos, toVideoInfoFromDTO(&list[i]))
	}
	return resp, nil
}

// UpdateVideoStatus 上下架视频；缺少 status 字段时返回参数错误。
func (s *GRPCVideoServer) UpdateVideoStatus(ctx context.Context, req *videov1.UpdateVideoStatusRequest) (*videov1.UpdateVideoStatusResponse, error) {
	if req.Status == nil {
		return nil, grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("缺少 status 字段"))
	}

	info, err := s.svc.UpdateStatus(ctx, req.GetId(), int8(*req.Status))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.UpdateVideoStatusResponse{Video: toVideoInfoFromDTO(info)}, nil
}

// RetryTranscode 重新转码一个失败视频（复用已上传的源文件）。
func (s *GRPCVideoServer) RetryTranscode(ctx context.Context, req *videov1.RetryTranscodeRequest) (*videov1.RetryTranscodeResponse, error) {
	info, err := s.svc.RetryTranscode(ctx, req.GetId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.RetryTranscodeResponse{Video: toVideoInfoFromDTO(info)}, nil
}

// UpdateVideo 编辑视频元信息（标题 / 简介 / 分类 / 封面）。
//
// 客户端流式：首帧为元信息，has_cover 为真时其后各帧是本次要替换的封面字节。
// 首帧到达前不触碰存储，避免流早退留下无主封面文件。
func (s *GRPCVideoServer) UpdateVideo(stream grpc.ClientStreamingServer[videov1.UpdateVideoFrame, videov1.UpdateVideoResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("缺少编辑元信息"))
		}
		return err
	}
	meta := first.GetMeta()
	if meta == nil {
		return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("首帧必须是编辑元信息"))
	}

	in := dto.UpdateVideoInput{
		Title:       meta.Title,
		Description: meta.Description,
		CategoryID:  meta.CategoryId,
	}

	if !meta.GetHasCover() {
		info, err := s.svc.Update(stream.Context(), meta.GetId(), in)
		if err != nil {
			return grpcerr.Wrap(err)
		}
		return stream.SendAndClose(&videov1.UpdateVideoResponse{Video: toVideoInfoFromDTO(info)})
	}

	// 封面走管道：泵协程把后续字节写入，Service 从读端消费。
	coverR, coverW := io.Pipe()
	in.CoverFilename = meta.GetCoverFilename()
	in.CoverSize = meta.GetCoverSize()
	in.Cover = coverR

	done := make(chan error, 1)
	go func() { done <- pumpChunks(coverW, recvUpdateChunk(stream)) }()

	info, err := s.svc.Update(stream.Context(), meta.GetId(), in)
	// 关闭读端以解除可能阻塞的写入，再等泵协程收尾，避免 goroutine 泄漏。
	_ = coverR.CloseWithError(io.ErrClosedPipe)
	<-done

	if err != nil {
		return grpcerr.Wrap(err)
	}
	return stream.SendAndClose(&videov1.UpdateVideoResponse{Video: toVideoInfoFromDTO(info)})
}

// DeleteVideo 删除视频及其源文件与封面；成功时回传字符串形式的 ID。
func (s *GRPCVideoServer) DeleteVideo(ctx context.Context, req *videov1.DeleteVideoRequest) (*videov1.DeleteVideoResponse, error) {
	if err := s.svc.Delete(ctx, req.GetId()); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.DeleteVideoResponse{Id: strconv.FormatInt(req.GetId(), 10)}, nil
}

// recvUpdateChunk 把 UpdateVideo 的后续帧规整为「字节 + error」，非数据帧按协议错误处理。
func recvUpdateChunk(stream grpc.ClientStreamingServer[videov1.UpdateVideoFrame, videov1.UpdateVideoResponse]) func() ([]byte, error) {
	return func() ([]byte, error) {
		f, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if chunk := f.GetChunk(); chunk != nil {
			return chunk, nil
		}
		return nil, errFrameFormat
	}
}

// ============================================================
// UploadService：单文件上传 + 分片上传全生命周期
// ============================================================

// GRPCUploadServer 是视频域上传契约的服务端实现。
type GRPCUploadServer struct {
	videov1.UnimplementedUploadServiceServer
	svc *service.Service
}

// NewGRPCUploadServer 构造 GRPCUploadServer。
func NewGRPCUploadServer(svc *service.Service) *GRPCUploadServer {
	return &GRPCUploadServer{svc: svc}
}

// UploadVideo 管理员上传整段视频。
//
// 客户端流式：首帧为元信息，其后先是视频文件字节，再是封面字节（可选）。
// meta.size 是文件与封面的字节边界；未带封面或大小未知时全部字节归文件。
func (s *GRPCUploadServer) UploadVideo(stream grpc.ClientStreamingServer[videov1.UploadVideoFrame, videov1.UploadVideoResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("缺少上传元信息"))
		}
		return err
	}
	meta := first.GetMeta()
	if meta == nil {
		return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("首帧必须是上传元信息"))
	}

	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(stream.Context())
	if userID <= 0 {
		return grpcerr.Wrap(errcode.ErrUnauthorized)
	}

	in := dto.UploadInput{
		Title:       meta.GetTitle(),
		Description: meta.GetDescription(),
		CategoryID:  meta.GetCategoryId(),
		Filename:    meta.GetFilename(),
		Size:        meta.GetSize(),
	}

	fileR, fileW := io.Pipe()
	in.File = fileR

	// 只有声明了文件大小且带封面时，才能把「文件字节」与「封面字节」切分开。
	useCover := meta.GetHasCover() && meta.GetSize() > 0
	var coverR *io.PipeReader
	var coverW *io.PipeWriter
	if useCover {
		coverR, coverW = io.Pipe()
		in.CoverFilename = meta.GetCoverFilename()
		in.CoverSize = meta.GetCoverSize()
		in.Cover = coverR
	}

	done := make(chan error, 1)
	go func() {
		done <- pumpUploadVideo(recvUploadChunk(stream), fileW, coverW, meta.GetSize(), meta.GetCoverSize())
	}()

	info, err := s.svc.Upload(stream.Context(), userID, in)
	_ = fileR.CloseWithError(io.ErrClosedPipe)
	if coverR != nil {
		_ = coverR.CloseWithError(io.ErrClosedPipe)
	}
	<-done

	if err != nil {
		return grpcerr.Wrap(err)
	}
	return stream.SendAndClose(&videov1.UploadVideoResponse{Video: toVideoInfoFromDTO(info)})
}

// InitUpload 初始化分片上传：命中秒传直接登记，命中上传中的会话回传进度。
func (s *GRPCUploadServer) InitUpload(ctx context.Context, req *videov1.InitUploadRequest) (*videov1.InitUploadResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	resp, err := s.svc.InitUpload(ctx, userID, dto.InitUploadRequest{
		FileMD5:     req.GetFileMd5(),
		Filename:    req.GetFilename(),
		TotalSize:   req.GetTotalSize(),
		ContentType: req.GetContentType(),
		Title:       req.GetTitle(),
		Description: req.GetDescription(),
		CategoryID:  req.GetCategoryId(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return toInitUploadProto(resp), nil
}

// UploadStatus 查询上传进度与已上传分片。
func (s *GRPCUploadServer) UploadStatus(ctx context.Context, req *videov1.UploadStatusRequest) (*videov1.UploadStatusResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	resp, err := s.svc.UploadStatus(ctx, userID, req.GetUploadId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return toUploadStatusProto(resp), nil
}

// PresignPart 获取某一片的预签名直传地址；驱动不支持时返回错误。
func (s *GRPCUploadServer) PresignPart(ctx context.Context, req *videov1.PresignPartRequest) (*videov1.PresignPartResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	resp, err := s.svc.PresignPart(ctx, userID, req.GetUploadId(), req.GetNumber())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.PresignPartResponse{PartNumber: resp.PartNumber, Url: resp.URL}, nil
}

// PutPart 服务端代传单个分片（客户端流式），供 local 驱动回退使用。
//
// meta.size 为该分片的字节数（-1 表示未知）；本周转的读端交给 Service 消费。
func (s *GRPCUploadServer) PutPart(stream grpc.ClientStreamingServer[videov1.PutPartFrame, videov1.PutPartResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("缺少分片元信息"))
		}
		return err
	}
	meta := first.GetMeta()
	if meta == nil {
		return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("首帧必须是分片元信息"))
	}

	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(stream.Context())
	if userID <= 0 {
		return grpcerr.Wrap(errcode.ErrUnauthorized)
	}

	partR, partW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- pumpChunks(partW, recvPutPartChunk(stream)) }()

	part, err := s.svc.PutPart(stream.Context(), userID, meta.GetUploadId(), meta.GetNumber(), partR, meta.GetSize())
	_ = partR.CloseWithError(io.ErrClosedPipe)
	<-done

	if err != nil {
		return grpcerr.Wrap(err)
	}
	return stream.SendAndClose(&videov1.PutPartResponse{Part: toUploadedPartProto(*part)})
}

// CompleteUpload 合并全部分片并登记视频。
func (s *GRPCUploadServer) CompleteUpload(ctx context.Context, req *videov1.CompleteUploadRequest) (*videov1.CompleteUploadResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	info, err := s.svc.CompleteUpload(ctx, userID, req.GetUploadId(), dto.CompleteUploadRequest{
		Parts: fromProtoParts(req.GetParts()),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.CompleteUploadResponse{Video: toVideoInfoFromDTO(info)}, nil
}

// AbortUpload 中止上传并释放分片；成功时回传字符串形式的会话 ID。
func (s *GRPCUploadServer) AbortUpload(ctx context.Context, req *videov1.AbortUploadRequest) (*videov1.AbortUploadResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	if err := s.svc.AbortUpload(ctx, userID, req.GetUploadId()); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.AbortUploadResponse{Id: strconv.FormatInt(req.GetUploadId(), 10)}, nil
}

// recvUploadChunk 把 UploadVideo 的后续帧规整为「字节 + error」，非数据帧按协议错误处理。
func recvUploadChunk(stream grpc.ClientStreamingServer[videov1.UploadVideoFrame, videov1.UploadVideoResponse]) func() ([]byte, error) {
	return func() ([]byte, error) {
		f, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if chunk := f.GetChunk(); chunk != nil {
			return chunk, nil
		}
		return nil, errFrameFormat
	}
}

// recvPutPartChunk 把 PutPart 的后续帧规整为「字节 + error」，非数据帧按协议错误处理。
func recvPutPartChunk(stream grpc.ClientStreamingServer[videov1.PutPartFrame, videov1.PutPartResponse]) func() ([]byte, error) {
	return func() ([]byte, error) {
		f, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if chunk := f.GetChunk(); chunk != nil {
			return chunk, nil
		}
		return nil, errFrameFormat
	}
}

// ============================================================
// ReportService：内容举报
// ============================================================

// GRPCReportServer 是视频域举报契约的服务端实现。
type GRPCReportServer struct {
	videov1.UnimplementedReportServiceServer
	svc *service.ReportService
}

// NewGRPCReportServer 构造 GRPCReportServer。
func NewGRPCReportServer(svc *service.ReportService) *GRPCReportServer {
	return &GRPCReportServer{svc: svc}
}

// CreateReport 发起举报（视频或评论）。
func (s *GRPCReportServer) CreateReport(ctx context.Context, req *videov1.CreateReportRequest) (*videov1.CreateReportResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	info, err := s.svc.Create(ctx, userID, dto.CreateReportInput{
		TargetType: int8(req.GetTargetType()),
		TargetID:   req.GetTargetId(),
		Reason:     req.GetReason(),
		Detail:     req.GetDetail(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.CreateReportResponse{Report: toReportInfoProto(info)}, nil
}

// ListReports 管理端分页查询举报列表。
func (s *GRPCReportServer) ListReports(ctx context.Context, req *videov1.ListReportsRequest) (*videov1.ListReportsResponse, error) {
	var status *int8
	if req.Status != nil {
		v := int8(*req.Status)
		status = &v
	}

	list, total, page, pageSize, err := s.svc.AdminList(ctx, dto.AdminReportListQuery{
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
		Status:   status,
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	resp := &videov1.ListReportsResponse{
		Reports:  make([]*videov1.ReportInfo, 0, len(list)),
		Total:    total,
		Page:     int32(page),
		PageSize: int32(pageSize),
	}
	for i := range list {
		resp.Reports = append(resp.Reports, toReportInfoProto(&list[i]))
	}
	return resp, nil
}

// HandleReport 管理端处理举报（已处理 / 已忽略）。
func (s *GRPCReportServer) HandleReport(ctx context.Context, req *videov1.HandleReportRequest) (*videov1.HandleReportResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	info, err := s.svc.Handle(ctx, req.GetId(), userID, int8(req.GetStatus()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &videov1.HandleReportResponse{Report: toReportInfoProto(info)}, nil
}

// ============================================================
// 流式泵送与契约映射
// ============================================================

// pumpChunks 从 recv 逐帧读取字节写入管道，直到读到 EOF。
//
// 无论正常结束还是出错都会关闭写端，向 Service 的读端发出结束信号。
func pumpChunks(w *io.PipeWriter, recv func() ([]byte, error)) error {
	for {
		chunk, err := recv()
		if errors.Is(err, io.EOF) {
			_ = w.Close()
			return nil
		}
		if err != nil {
			_ = w.CloseWithError(err)
			return err
		}
		if len(chunk) == 0 {
			continue
		}
		if _, err := w.Write(chunk); err != nil {
			_ = w.CloseWithError(err)
			return err
		}
	}
}

// pumpUploadVideo 按「先文件后封面」的顺序把上传帧的字节泵入两条管道。
//
// fileSize 是视频文件的字节边界：文件写满即关闭文件管道，其后的字节归封面；
// coverW 为 nil（未带封面或大小未知）时全部字节归文件。超出两段总量的字节丢弃。
func pumpUploadVideo(recv func() ([]byte, error), fileW, coverW *io.PipeWriter, fileSize, coverSize int64) error {
	if coverW == nil {
		return pumpChunks(fileW, recv)
	}

	fileLeft := fileSize
	coverLeft := coverSize
	for {
		chunk, err := recv()
		if errors.Is(err, io.EOF) {
			_ = fileW.Close()
			_ = coverW.Close()
			return nil
		}
		if err != nil {
			_ = fileW.CloseWithError(err)
			_ = coverW.CloseWithError(err)
			return err
		}

		// 文件段：写满 fileSize 字节后关闭文件管道，此后字节转封面
		if len(chunk) > 0 && fileLeft > 0 {
			n := int64(len(chunk))
			if n > fileLeft {
				n = fileLeft
			}
			if _, err := fileW.Write(chunk[:n]); err != nil {
				_ = coverW.CloseWithError(err)
				return err
			}
			chunk = chunk[n:]
			fileLeft -= n
			if fileLeft == 0 {
				_ = fileW.Close()
			}
		}

		// 封面段：只取前 coverSize 字节，多余字节丢弃
		if len(chunk) > 0 && coverLeft > 0 {
			n := int64(len(chunk))
			if n > coverLeft {
				n = coverLeft
			}
			if _, err := coverW.Write(chunk[:n]); err != nil {
				return err
			}
			chunk = chunk[n:]
			coverLeft -= n
			if coverLeft == 0 {
				_ = coverW.Close()
			}
		}
	}
}

// toVideoBrief 把视频模型映射为「卡片字段」契约，供推荐 / 搜索 / 通知等场景使用。
//
// 带上 duration / view_count / created_at：首页卡片要展示时长角标、播放量与发布时间，
// 让首页流仅凭本结构就能出图，不必再按 id 回查 VideoInfo。
func toVideoBrief(v *model.Video) *videov1.VideoBrief {
	return &videov1.VideoBrief{
		Id:         v.ID,
		Title:      v.Title,
		UploaderId: v.UploaderID,
		Status:     int32(v.Status),
		CoverUrl:   v.CoverURL,
		CategoryId: v.CategoryID,
		Duration:   int32(v.Duration),
		ViewCount:  v.ViewCount,
		CreatedAt:  timestamppb.New(v.CreatedAt),
	}
}

// toVideoInfo 把视频模型映射为完整契约，供读路径回填对外响应。
//
// 需要 description / play_url / 统计数 / created_at 等字段，这样网关把 gRPC
// 结果映射回对外 JSON 时才能与进程内直调完全一致。
func toVideoInfo(v *model.Video) *videov1.VideoInfo {
	return &videov1.VideoInfo{
		Id:            v.ID,
		Title:         v.Title,
		UploaderId:    v.UploaderID,
		Status:        int32(v.Status),
		CoverUrl:      v.CoverURL,
		CategoryId:    v.CategoryID,
		Description:   v.Description,
		PlayUrl:       v.PlayURL,
		Duration:      int32(v.Duration),
		ViewCount:     v.ViewCount,
		LikeCount:     v.LikeCount,
		FavoriteCount: v.FavoriteCount,
		ErrorMsg:      v.ErrorMsg,
		CreatedAt:     timestamppb.New(v.CreatedAt),
	}
}

// toVideoInfoFromDTO 把对外 DTO 映射为完整契约，供管理端写操作的响应回填。
func toVideoInfoFromDTO(v *dto.VideoInfo) *videov1.VideoInfo {
	return &videov1.VideoInfo{
		Id:            v.ID,
		Title:         v.Title,
		Status:        int32(v.Status),
		CoverUrl:      v.CoverURL,
		CategoryId:    v.CategoryID,
		Description:   v.Description,
		PlayUrl:       v.PlayURL,
		Duration:      int32(v.Duration),
		ViewCount:     v.ViewCount,
		LikeCount:     v.LikeCount,
		FavoriteCount: v.FavoriteCount,
		ErrorMsg:      v.ErrorMsg,
		CreatedAt:     timestamppb.New(v.CreatedAt),
	}
}

// toUploadedPartProto 把分片映射为契约结构。
func toUploadedPartProto(p dto.UploadedPart) *videov1.UploadedPart {
	return &videov1.UploadedPart{PartNumber: p.PartNumber, Etag: p.ETag, Size: p.Size}
}

// toUploadedPartsProto 批量把分片映射为契约结构；空切片返回空切片而非 nil，保持 JSON 为 []。
func toUploadedPartsProto(parts []dto.UploadedPart) []*videov1.UploadedPart {
	list := make([]*videov1.UploadedPart, 0, len(parts))
	for i := range parts {
		list = append(list, toUploadedPartProto(parts[i]))
	}
	return list
}

// fromProtoParts 把契约结构的分片还原为对外 DTO。
func fromProtoParts(parts []*videov1.UploadedPart) []dto.UploadedPart {
	list := make([]dto.UploadedPart, 0, len(parts))
	for _, p := range parts {
		list = append(list, dto.UploadedPart{
			PartNumber: p.GetPartNumber(),
			ETag:       p.GetEtag(),
			Size:       p.GetSize(),
		})
	}
	return list
}

// toInitUploadProto 把上传会话结果映射为契约结构。
func toInitUploadProto(r *dto.InitUploadResponse) *videov1.InitUploadResponse {
	return &videov1.InitUploadResponse{
		Mode:      r.Mode,
		UploadId:  r.UploadID,
		ChunkSize: r.ChunkSize,
		PartTotal: int32(r.PartTotal),
		Uploaded:  toUploadedPartsProto(r.Uploaded),
		VideoId:   r.VideoID,
	}
}

// toUploadStatusProto 把上传进度映射为契约结构。
func toUploadStatusProto(r *dto.InitUploadResponse) *videov1.UploadStatusResponse {
	return &videov1.UploadStatusResponse{
		Mode:      r.Mode,
		UploadId:  r.UploadID,
		ChunkSize: r.ChunkSize,
		PartTotal: int32(r.PartTotal),
		Uploaded:  toUploadedPartsProto(r.Uploaded),
		VideoId:   r.VideoID,
	}
}

// toReportInfoProto 把举报 DTO 映射为契约结构；handled_at 为空时留空。
func toReportInfoProto(r *dto.ReportInfo) *videov1.ReportInfo {
	p := &videov1.ReportInfo{
		Id:               r.ID,
		TargetType:       int32(r.TargetType),
		TargetId:         r.TargetID,
		VideoId:          r.VideoID,
		VideoTitle:       r.VideoTitle,
		ReporterId:       r.ReporterID,
		ReporterNickname: r.ReporterNickname,
		Reason:           r.Reason,
		Detail:           r.Detail,
		Status:           int32(r.Status),
		CreatedAt:        timestamppb.New(r.CreatedAt),
	}
	if r.HandledAt != nil {
		p.HandledAt = timestamppb.New(*r.HandledAt)
	}
	return p
}
