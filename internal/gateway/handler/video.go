// Package handler 承载网关侧的 BFF 处理器：把外部 HTTP 请求翻译成对内部服务的 gRPC 调用，
// 再把结果映射回与进程内直调完全一致的对外 JSON，保证前端无感。
package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/response"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// sortHot 是 gRPC 契约里「按热度排序」的取值，与外部查询参数 sort=hot 对应。
const sortHot int32 = 1

// VideoReadHandler 是视频读路径的网关处理器，经 gRPC 客户端访问视频服务。
type VideoReadHandler struct {
	client videov1.VideoServiceClient
}

// NewVideoReadHandler 构造 VideoReadHandler。
func NewVideoReadHandler(client videov1.VideoServiceClient) *VideoReadHandler {
	return &VideoReadHandler{client: client}
}

// List 分页查询视频列表：GET /api/v1/videos
//
// 查询参数与改造前一致：keyword 可选（标题或简介模糊匹配），
// sort 传 hot 按热度、其余（含空）按最新。分页沿用 utils.NormalizePage 的归一规则，
// 响应的分页字段与请求口径一致地回填。
func (h *VideoReadHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	categoryID, _ := strconv.ParseInt(c.Query("category_id"), 10, 64)

	var sort int32
	if c.Query("sort") == dto.SortHot {
		sort = sortHot
	}

	resp, err := h.client.ListVideos(c.Request.Context(), &videov1.ListVideosRequest{
		CategoryId: categoryID,
		Keyword:    c.Query("keyword"),
		Sort:       sort,
		Page:       int32(page),
		PageSize:   int32(pageSize),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	list := make([]dto.VideoInfo, 0, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		list = append(list, toVideoInfo(v))
	}

	p, ps, _ := utils.NormalizePage(page, pageSize)
	response.OKPage(c, list, resp.GetTotal(), p, ps)
}

// Detail 查询视频详情：GET /api/v1/videos/{id}
//
// 仅已上架视频对普通用户可见，与改造前口径一致。
func (h *VideoReadHandler) Detail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	resp, err := h.client.GetVideo(c.Request.Context(), &videov1.GetVideoRequest{Id: id})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	info := toVideoInfo(resp.GetVideo())
	if info.Status != model.StatusPublished {
		response.Fail(c, errcode.ErrVideoOffline)
		return
	}

	// 播放量自增属统计行为：成功则回填 +1，失败不影响详情返回（与改造前一致）。
	if _, err := h.client.IncrViewCount(c.Request.Context(), &videov1.IncrViewCountRequest{VideoId: id}); err == nil {
		info.ViewCount++
	}
	response.OK(c, info)
}

// failFromGRPC 把 gRPC 错误映射为对外响应。
//
// 优先用服务端编码进 status.message 的业务错误码原样还原，保证 HTTP 状态码与响应 code
// 与改造前逐字一致；读路径旧 RPC 仍以 codes.NotFound + 纯文本表达「视频不存在」，走兜底；
// 其余按系统异常统一 500。
func failFromGRPC(c *gin.Context, err error) {
	if ec, ok := grpcerr.Unwrap(err); ok {
		response.FailWithMsg(c, ec, ec.Message)
		return
	}
	if status.Code(err) == codes.NotFound {
		response.Fail(c, errcode.ErrVideoNotFound)
		return
	}
	response.FailErr(c, err)
}

// toVideoInfo 把跨服务契约的完整视频信息映射回对外 DTO，字段与序列化口径均保持不变。
func toVideoInfo(v *videov1.VideoInfo) dto.VideoInfo {
	info := dto.VideoInfo{
		ID:            v.GetId(),
		Title:         v.GetTitle(),
		Description:   v.GetDescription(),
		CoverURL:      v.GetCoverUrl(),
		PlayURL:       v.GetPlayUrl(),
		Duration:      int(v.GetDuration()),
		CategoryID:    v.GetCategoryId(),
		ViewCount:     v.GetViewCount(),
		LikeCount:     v.GetLikeCount(),
		FavoriteCount: v.GetFavoriteCount(),
		Status:        int8(v.GetStatus()),
		ErrorMsg:      v.GetErrorMsg(),
	}
	// created_at 经 gRPC 传为 UTC 时间戳，转回本地时区，序列化结果与直读数据库一致。
	if ts := v.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}

// toVideoBrief 把跨服务契约的「精简卡片」映射回对外 DTO。
//
// 推荐流（/feed）与搜索（/search/videos）返回的都是 video.v1.VideoBrief，映射口径相同，
// 因此收敛到这里；字段与序列化口径与 toVideoInfo 保持一致。
func toVideoBrief(v *videov1.VideoBrief) dto.VideoBrief {
	brief := dto.VideoBrief{
		ID:         v.GetId(),
		Title:      v.GetTitle(),
		UploaderID: v.GetUploaderId(),
		CoverURL:   v.GetCoverUrl(),
		CategoryID: v.GetCategoryId(),
		Status:     int8(v.GetStatus()),
		Duration:   int(v.GetDuration()),
		ViewCount:  v.GetViewCount(),
	}
	// created_at 经 gRPC 传为 UTC 时间戳，转回本地时区，序列化结果与直读数据库一致。
	if ts := v.GetCreatedAt(); ts != nil {
		brief.CreatedAt = ts.AsTime().In(time.Local)
	}
	return brief
}
