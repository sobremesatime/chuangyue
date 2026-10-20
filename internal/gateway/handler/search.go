package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	searchv1 "github.com/sobremesatime/chuangyue/api/gen/search/v1"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/response"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// SearchHandler 是视频检索的网关处理器，经 gRPC 客户端访问搜索服务。
type SearchHandler struct {
	client searchv1.SearchServiceClient
}

// NewSearchHandler 构造 SearchHandler。
func NewSearchHandler(client searchv1.SearchServiceClient) *SearchHandler {
	return &SearchHandler{client: client}
}

// Videos 按关键词检索视频：GET /api/v1/search/videos
//
// 公开接口，不需要登录。keyword 为空等价于「不按关键词过滤」，category_id 为 0 等价于
// 「不按分类过滤」，两者是「与」关系。
// 与其它列表接口一致：先按统一口径归一分页，再把归一后的值下发给搜索服务，
// 保证响应里的 page / page_size 与实际返回条数严格对应。
func (h *SearchHandler) Videos(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	categoryID, _ := strconv.ParseInt(c.Query("category_id"), 10, 64)

	p, ps, _ := utils.NormalizePage(page, pageSize)

	resp, err := h.client.SearchVideos(c.Request.Context(), &searchv1.SearchVideosRequest{
		Keyword:    c.Query("keyword"),
		CategoryId: categoryID,
		Page:       int32(p),
		PageSize:   int32(ps),
	})
	if err != nil {
		// 检索失败都是系统级故障（服务端以 codes.Internal 表达，没有业务错误码），
		// 交给 FailErr 统一记录日志并返回 500，避免把内部细节透给前端。
		response.FailErr(c, err)
		return
	}

	list := make([]videodto.VideoBrief, 0, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		list = append(list, toVideoBrief(v))
	}
	response.OKPage(c, list, resp.GetTotal(), p, ps)
}
