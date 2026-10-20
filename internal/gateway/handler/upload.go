package handler

import (
	"io"
	"strconv"

	"github.com/gin-gonic/gin"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// UploadHandler 是管理端上传（单文件 + 分片）的网关处理器，经 gRPC 客户端访问视频服务。
type UploadHandler struct {
	client videov1.UploadServiceClient
}

// NewUploadHandler 构造 UploadHandler。
func NewUploadHandler(client videov1.UploadServiceClient) *UploadHandler {
	return &UploadHandler{client: client}
}

// Upload 管理员上传视频：POST /api/v1/admin/videos
//
// 使用 multipart/form-data，字段：file（必填）、title（必填）、
// description、category_id、cover（可选）。文件与封面走客户端流式发给视频服务。
func (h *UploadHandler) Upload(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "缺少视频文件")
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		response.Fail(c, errcode.ErrUploadFailed)
		return
	}
	defer file.Close()

	meta := &videov1.UploadVideoMeta{
		Title:       c.PostForm("title"),
		Description: c.PostForm("description"),
		CategoryId:  parseCategoryID(c.PostForm("category_id")),
		Filename:    fileHeader.Filename,
		Size:        fileHeader.Size,
	}

	var cover io.ReadCloser
	if coverHeader, err := c.FormFile("cover"); err == nil {
		if f, err := coverHeader.Open(); err == nil {
			cover = f
			meta.HasCover = true
			meta.CoverFilename = coverHeader.Filename
			meta.CoverSize = coverHeader.Size
		}
	}
	if cover != nil {
		defer cover.Close()
	}

	stream, err := h.client.UploadVideo(c.Request.Context())
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	if err := stream.Send(&videov1.UploadVideoFrame{Payload: &videov1.UploadVideoFrame_Meta{Meta: meta}}); err != nil {
		failFromGRPC(c, err)
		return
	}
	// 先发文件字节，再发封面字节：服务端按 meta.size 切分两段。
	if err := sendChunks(func(chunk []byte) error {
		return stream.Send(&videov1.UploadVideoFrame{Payload: &videov1.UploadVideoFrame_Chunk{Chunk: chunk}})
	}, file); err != nil {
		failFromGRPC(c, err)
		return
	}
	if cover != nil {
		if err := sendChunks(func(chunk []byte) error {
			return stream.Send(&videov1.UploadVideoFrame{Payload: &videov1.UploadVideoFrame_Chunk{Chunk: chunk}})
		}, cover); err != nil {
			failFromGRPC(c, err)
			return
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toVideoInfo(resp.GetVideo()))
}

// InitUpload 初始化分片上传：POST /api/v1/admin/uploads
//
// 请求体为 JSON，字段见 dto.InitUploadRequest。命中秒传直接返回 video_id。
func (h *UploadHandler) InitUpload(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	var in dto.InitUploadRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	resp, err := h.client.InitUpload(c.Request.Context(), &videov1.InitUploadRequest{
		FileMd5:     in.FileMD5,
		Filename:    in.Filename,
		TotalSize:   in.TotalSize,
		ContentType: in.ContentType,
		Title:       in.Title,
		Description: in.Description,
		CategoryId:  in.CategoryID,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, initUploadToDTO(resp))
}

// UploadStatus 查询上传进度与已上传分片：GET /api/v1/admin/uploads/{id}
func (h *UploadHandler) UploadStatus(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}
	uploadID, err := parseUploadID(c)
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的上传会话 ID")
		return
	}

	resp, err := h.client.UploadStatus(c.Request.Context(), &videov1.UploadStatusRequest{
		UploadId: uploadID,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, uploadStatusToDTO(resp))
}

// PresignPart 获取分片预签名直传地址：POST /api/v1/admin/uploads/{id}/parts/{n}/presign
func (h *UploadHandler) PresignPart(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}
	uploadID, err := parseUploadID(c)
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的上传会话 ID")
		return
	}
	number, err := parsePartNumber(c)
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的分片序号")
		return
	}

	resp, err := h.client.PresignPart(c.Request.Context(), &videov1.PresignPartRequest{
		UploadId: uploadID,
		Number:   number,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, dto.PresignPartResponse{PartNumber: resp.GetPartNumber(), URL: resp.GetUrl()})
}

// PutPart 上传单个分片（请求体为原始二进制）：PUT /api/v1/admin/uploads/{id}/parts/{n}
//
// 分片字节经客户端流式转发给视频服务；size 取 Content-Length，chunked 编码下为 -1（未知）。
func (h *UploadHandler) PutPart(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}
	uploadID, err := parseUploadID(c)
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的上传会话 ID")
		return
	}
	number, err := parsePartNumber(c)
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的分片序号")
		return
	}

	stream, err := h.client.PutPart(c.Request.Context())
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	meta := &videov1.PutPartMeta{
		UploadId: uploadID,
		Number:   number,
		Size:     c.Request.ContentLength,
	}
	if err := stream.Send(&videov1.PutPartFrame{Payload: &videov1.PutPartFrame_Meta{Meta: meta}}); err != nil {
		failFromGRPC(c, err)
		return
	}
	if err := sendChunks(func(chunk []byte) error {
		return stream.Send(&videov1.PutPartFrame{Payload: &videov1.PutPartFrame_Chunk{Chunk: chunk}})
	}, c.Request.Body); err != nil {
		failFromGRPC(c, err)
		return
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	part := resp.GetPart()
	response.OK(c, dto.UploadedPart{
		PartNumber: part.GetPartNumber(),
		ETag:       part.GetEtag(),
		Size:       part.GetSize(),
	})
}

// CompleteUpload 完成分片上传：POST /api/v1/admin/uploads/{id}/complete
//
// 请求体可为空；解析失败按空体处理，以服务端已记录的分片为准。
func (h *UploadHandler) CompleteUpload(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}
	uploadID, err := parseUploadID(c)
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的上传会话 ID")
		return
	}

	var in dto.CompleteUploadRequest
	_ = c.ShouldBindJSON(&in)

	resp, err := h.client.CompleteUpload(c.Request.Context(), &videov1.CompleteUploadRequest{
		UploadId: uploadID,
		Parts:    toProtoUploadedParts(in.Parts),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toVideoInfo(resp.GetVideo()))
}

// AbortUpload 中止分片上传：DELETE /api/v1/admin/uploads/{id}
func (h *UploadHandler) AbortUpload(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}
	uploadID, err := parseUploadID(c)
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的上传会话 ID")
		return
	}

	resp, err := h.client.AbortUpload(c.Request.Context(), &videov1.AbortUploadRequest{
		UploadId: uploadID,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, gin.H{"id": resp.GetId()})
}

// parseUploadID 解析路径中的上传会话 ID（路径参数 id）。
func parseUploadID(c *gin.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errcode.ErrInvalidParam
	}
	return id, nil
}

// parsePartNumber 解析路径中的分片序号（路径参数 n）。
func parsePartNumber(c *gin.Context) (int32, error) {
	n, err := strconv.ParseInt(c.Param("n"), 10, 32)
	if err != nil || n <= 0 {
		return 0, errcode.ErrInvalidParam
	}
	return int32(n), nil
}

// toProtoUploadedParts 把对外分片结构转成契约结构。
func toProtoUploadedParts(parts []dto.UploadedPart) []*videov1.UploadedPart {
	list := make([]*videov1.UploadedPart, 0, len(parts))
	for _, p := range parts {
		list = append(list, &videov1.UploadedPart{PartNumber: p.PartNumber, Etag: p.ETag, Size: p.Size})
	}
	return list
}

// initUploadToDTO 把契约的初始化结果映射回对外 DTO，序列化口径与改造前一致。
func initUploadToDTO(r *videov1.InitUploadResponse) dto.InitUploadResponse {
	out := dto.InitUploadResponse{
		Mode:      r.GetMode(),
		UploadID:  r.GetUploadId(),
		ChunkSize: r.GetChunkSize(),
		PartTotal: int(r.GetPartTotal()),
		VideoID:   r.GetVideoId(),
	}
	if parts := r.GetUploaded(); len(parts) > 0 {
		out.Uploaded = make([]dto.UploadedPart, 0, len(parts))
		for _, p := range parts {
			out.Uploaded = append(out.Uploaded, dto.UploadedPart{
				PartNumber: p.GetPartNumber(),
				ETag:       p.GetEtag(),
				Size:       p.GetSize(),
			})
		}
	}
	return out
}

// uploadStatusToDTO 把契约的上传进度映射回对外 DTO，序列化口径与改造前一致。
func uploadStatusToDTO(r *videov1.UploadStatusResponse) dto.InitUploadResponse {
	out := dto.InitUploadResponse{
		Mode:      r.GetMode(),
		UploadID:  r.GetUploadId(),
		ChunkSize: r.GetChunkSize(),
		PartTotal: int(r.GetPartTotal()),
		VideoID:   r.GetVideoId(),
	}
	if parts := r.GetUploaded(); len(parts) > 0 {
		out.Uploaded = make([]dto.UploadedPart, 0, len(parts))
		for _, p := range parts {
			out.Uploaded = append(out.Uploaded, dto.UploadedPart{
				PartNumber: p.GetPartNumber(),
				ETag:       p.GetEtag(),
				Size:       p.GetSize(),
			})
		}
	}
	return out
}
