package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	interactiondto "github.com/sobremesatime/chuangyue/internal/interaction/dto"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// DanmakuHandler 是视频弹幕能力的网关处理器，经 gRPC 客户端访问互动服务。
type DanmakuHandler struct {
	client interactionv1.DanmakuServiceClient
}

// NewDanmakuHandler 构造 DanmakuHandler。
func NewDanmakuHandler(client interactionv1.DanmakuServiceClient) *DanmakuHandler {
	return &DanmakuHandler{client: client}
}

// List 拉取某视频的全部弹幕：GET /api/v1/videos/{id}/danmaku
//
// 公开接口，不需要登录。
func (h *DanmakuHandler) List(c *gin.Context) {
	videoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || videoID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	resp, err := h.client.ListDanmaku(c.Request.Context(), &interactionv1.ListDanmakuRequest{VideoId: videoID})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}

	list := make([]interactiondto.DanmakuInfo, 0, len(resp.GetDanmaku()))
	for _, d := range resp.GetDanmaku() {
		list = append(list, toDanmakuInfoDTO(d))
	}
	response.OK(c, list)
}

// Create 发表弹幕：POST /api/v1/videos/{id}/danmaku
//
// 请求体 {"content":"...","time_ms":1234,"color":"#FFFFFF","mode":1}，需要登录。
func (h *DanmakuHandler) Create(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	videoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || videoID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	var in interactiondto.CreateDanmakuInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "请求体格式错误")
		return
	}

	resp, err := h.client.CreateDanmaku(c.Request.Context(), &interactionv1.CreateDanmakuRequest{
		VideoId: videoID,
		Content: in.Content,
		TimeMs:  int32(in.TimeMs),
		Color:   in.Color,
		Mode:    int32(in.Mode),
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, toDanmakuInfoDTO(resp.GetDanmaku()))
}

// Delete 删除弹幕：DELETE /api/v1/danmaku/{id}
//
// 仅发送者本人或管理员可删，需要登录。
func (h *DanmakuHandler) Delete(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	danmakuID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || danmakuID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的弹幕 ID")
		return
	}

	if _, err := h.client.DeleteDanmaku(c.Request.Context(), &interactionv1.DeleteDanmakuRequest{
		DanmakuId: danmakuID,
	}); err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	response.OK(c, nil)
}

// AdminList 分页查询全部弹幕，供管理端内容治理：GET /api/v1/admin/danmaku
//
// keyword 查询参数可选，按弹幕内容模糊匹配。
func (h *DanmakuHandler) AdminList(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := h.client.AdminListDanmaku(c.Request.Context(), &interactionv1.AdminListDanmakuRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
		Keyword:  c.Query("keyword"),
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}

	list := make([]videodto.AdminDanmakuInfo, 0, len(resp.GetDanmaku()))
	for _, d := range resp.GetDanmaku() {
		list = append(list, toAdminDanmakuInfoDTO(d))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// AdminDelete 管理端删除弹幕：DELETE /api/v1/admin/danmaku/{id}
//
// 采用软删保留记录。需要登录且为管理员（由路由的 RequireRole 保证）。
func (h *DanmakuHandler) AdminDelete(c *gin.Context) {
	danmakuID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || danmakuID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的弹幕 ID")
		return
	}

	resp, err := h.client.AdminDeleteDanmaku(c.Request.Context(), &interactionv1.AdminDeleteDanmakuRequest{
		DanmakuId: danmakuID,
	})
	if err != nil {
		failFromInteractionGRPC(c, err)
		return
	}
	// 雪花 ID 以字符串回传，与列表、详情保持一致
	response.OK(c, gin.H{"id": strconv.FormatInt(resp.GetId(), 10)})
}
