package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	feedv1 "github.com/sobremesatime/chuangyue/api/gen/feed/v1"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/response"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// FeedHandler 是首页推荐流的网关处理器，经 gRPC 客户端访问推荐服务。
type FeedHandler struct {
	client feedv1.FeedServiceClient
}

// NewFeedHandler 构造 FeedHandler。
func NewFeedHandler(client feedv1.FeedServiceClient) *FeedHandler {
	return &FeedHandler{client: client}
}

// Home 首页推荐流：GET /api/v1/feed
//
// 公开接口：未登录也能看，此时推荐服务按全局热度回退；已登录则带上 user_id 做个性化重排。
// 返回条目为视频精简卡片（标题 / 封面 / 分类等），字段口径与其它列表接口一致。
func (h *FeedHandler) Home(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	// 先按网关统一口径归一，再把归一后的值下发给推荐服务：推荐服务自身也有一套兜底规则
	// （超限时回到默认值），预先归一可避免两边口径不一致、导致回填的 page_size 与实际条数对不上。
	p, ps, _ := utils.NormalizePage(page, pageSize)

	resp, err := h.client.GetHomeFeed(c.Request.Context(), &feedv1.GetHomeFeedRequest{
		Page:     int32(p),
		PageSize: int32(ps),
	})
	if err != nil {
		// 推荐流的失败都是系统级故障（服务端以 codes.Internal 表达，没有业务错误码），
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
