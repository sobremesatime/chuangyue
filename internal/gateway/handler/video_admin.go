package handler

import (
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// streamChunkSize 是流式 RPC 单帧承载的字节数。
//
// 取 1MB：远低于 gRPC 单条消息默认上限 4MB，同时保持吞吐，避免超大消息被拒。
const streamChunkSize = 1 << 20

// VideoAdminHandler 是管理端视频 CRUD 的网关处理器，经 gRPC 客户端访问视频服务。
type VideoAdminHandler struct {
	client videov1.VideoServiceClient
}

// NewVideoAdminHandler 构造 VideoAdminHandler。
func NewVideoAdminHandler(client videov1.VideoServiceClient) *VideoAdminHandler {
	return &VideoAdminHandler{client: client}
}

// AdminList 分页查询全部视频（含未上架）：GET /api/v1/admin/videos
//
// status 查询参数可选，取值 0 转码中 / 1 已上架 / 2 已下架 / 3 转码失败，不传则返回全部。
func (h *VideoAdminHandler) AdminList(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	status, err := parseStatusQuery(c.Query("status"))
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频状态")
		return
	}

	resp, err := h.client.AdminListVideos(c.Request.Context(), &videov1.AdminListVideosRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
		Status:   toProtoInt32(status),
		Keyword:  strings.TrimSpace(c.Query("keyword")),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	list := make([]dto.VideoInfo, 0, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		list = append(list, toVideoInfo(v))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// UpdateStatus 上架 / 下架视频：PATCH /api/v1/admin/videos/{id}
//
// 请求体 {"status": 1|2}，1 表示上架，2 表示下架。
func (h *VideoAdminHandler) UpdateStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	var in dto.UpdateStatusInput
	if err := c.ShouldBindJSON(&in); err != nil || in.Status == nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "缺少 status 字段")
		return
	}

	resp, err := h.client.UpdateVideoStatus(c.Request.Context(), &videov1.UpdateVideoStatusRequest{
		Id:     id,
		Status: toProtoInt32(in.Status),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toVideoInfo(resp.GetVideo()))
}

// RetryTranscode 重新转码失败视频：POST /api/v1/admin/videos/{id}/transcode
//
// 不需要请求体：源文件在上传时就落盘了，重试只是把状态打回「转码中」并重新入队。
// 只对 status=3（转码失败）生效，其余状态由服务端拒绝并给出中文提示。
func (h *VideoAdminHandler) RetryTranscode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	resp, err := h.client.RetryTranscode(c.Request.Context(), &videov1.RetryTranscodeRequest{Id: id})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toVideoInfo(resp.GetVideo()))
}

// Update 编辑视频：PUT /api/v1/admin/videos/{id}
//
// 使用 multipart/form-data，字段 title、description、category_id、cover 均可选，
// 传了才改（description 传空串表示清空简介）。封面走客户端流式，片源不可替换。
func (h *VideoAdminHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	meta := &videov1.UpdateVideoMeta{Id: id}
	if title, exists := c.GetPostForm("title"); exists {
		meta.Title = &title
	}
	if description, exists := c.GetPostForm("description"); exists {
		meta.Description = &description
	}
	if raw, exists := c.GetPostForm("category_id"); exists {
		categoryID := parseCategoryID(raw)
		meta.CategoryId = &categoryID
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

	stream, err := h.client.UpdateVideo(c.Request.Context())
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	if err := stream.Send(&videov1.UpdateVideoFrame{Payload: &videov1.UpdateVideoFrame_Meta{Meta: meta}}); err != nil {
		failFromGRPC(c, err)
		return
	}
	if cover != nil {
		err := sendChunks(func(chunk []byte) error {
			return stream.Send(&videov1.UpdateVideoFrame{Payload: &videov1.UpdateVideoFrame_Chunk{Chunk: chunk}})
		}, cover)
		if err != nil {
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

// Delete 删除视频：DELETE /api/v1/admin/videos/{id}
//
// 不可恢复：数据库记录与磁盘上的视频、封面文件都会被清理。
func (h *VideoAdminHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	resp, err := h.client.DeleteVideo(c.Request.Context(), &videov1.DeleteVideoRequest{Id: id})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, gin.H{"id": resp.GetId()})
}

// sendChunks 以固定大小的帧把 r 的内容交给 send，直到读完。
//
// 单次 Read 可能同时返回数据与 io.EOF，因此先发送已读部分再判断结束。
func sendChunks(send func(chunk []byte) error, r io.Reader) error {
	buf := make([]byte, streamChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if sendErr := send(buf[:n]); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// toProtoInt32 把「未传 / 已传」的 int8 指针转成契约里的可空 int32 字段。
//
// 用户管理与视频管理都要做同样的转换，因此放在这里共用（同包内可见）。
func toProtoInt32(v *int8) *int32 {
	if v == nil {
		return nil
	}
	out := int32(*v)
	return &out
}

// parseStatusQuery 解析状态查询参数，空串表示不按状态过滤（返回 nil）。
//
// 合法取值为 0 转码中 / 1 已上架 / 2 已下架 / 3 转码失败。
// 非法取值返回错误，由调用方决定如何提示；不用 0 兜底是因为
// 0（转码中）是合法状态，兜底会把「参数写错」静默变成「只看转码中」。
func parseStatusQuery(raw string) (*int8, error) {
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 8)
	if err != nil || v < 0 || v > 3 {
		return nil, errcode.ErrInvalidParam
	}
	status := int8(v)
	return &status, nil
}

// parseCategoryID 解析分类 ID，非法值统一按 0（不过滤）处理。
func parseCategoryID(raw string) int64 {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}
