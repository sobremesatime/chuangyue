package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// collectionPage 是合集列表的分页响应。
//
// 没有复用 response.Page：合集前端约定分页字段为 items，与其它列表接口的 list 不同。
// 单独定义把这一处差异收敛在合集 Handler 里，避免改动全局分页结构影响其它接口。
type collectionPage struct {
	Items    []dto.CollectionInfo `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

// CollectionReadHandler 是合集公开读路径的网关处理器，经 gRPC 客户端访问视频服务。
type CollectionReadHandler struct {
	client videov1.VideoServiceClient
}

// NewCollectionReadHandler 构造 CollectionReadHandler。
func NewCollectionReadHandler(client videov1.VideoServiceClient) *CollectionReadHandler {
	return &CollectionReadHandler{client: client}
}

// List 分页查询公开合集：GET /api/v1/collections
//
// 只返回 status=1 的合集；封面为空时由服务端用合集内第一个已上架视频的封面兜底。
func (h *CollectionReadHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := h.client.ListCollections(c.Request.Context(), &videov1.ListCollectionsRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	list := make([]dto.CollectionInfo, 0, len(resp.GetCollections()))
	for _, v := range resp.GetCollections() {
		list = append(list, toCollectionInfo(v))
	}
	response.OK(c, collectionPage{
		Items:    list,
		Total:    resp.GetTotal(),
		Page:     int(resp.GetPage()),
		PageSize: int(resp.GetPageSize()),
	})
}

// Detail 查询公开合集详情：GET /api/v1/collections/{id}
//
// 返回合集信息与有序播放列表（只含已上架视频）；草稿合集按「不存在」处理。
func (h *CollectionReadHandler) Detail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的合集 ID")
		return
	}

	resp, err := h.client.GetCollection(c.Request.Context(), &videov1.GetCollectionRequest{Id: id})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	videos := make([]dto.VideoInfo, 0, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		videos = append(videos, toVideoInfo(v))
	}
	response.OK(c, dto.CollectionDetail{
		CollectionInfo: toCollectionInfo(resp.GetCollection()),
		Videos:         videos,
	})
}

// ByVideo 反查视频所属的公开合集：GET /api/v1/videos/{id}/collections
func (h *CollectionReadHandler) ByVideo(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	resp, err := h.client.GetCollectionByVideo(c.Request.Context(), &videov1.GetCollectionByVideoRequest{
		VideoId: id,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	list := make([]dto.CollectionBrief, 0, len(resp.GetCollections()))
	for _, v := range resp.GetCollections() {
		list = append(list, toCollectionBrief(v))
	}
	response.OK(c, list)
}

// CollectionAdminHandler 是管理端合集 CRUD 的网关处理器。
//
// 路由挂在 admin 分组下，登录与角色校验由中间件完成，这里只做 HTTP ↔ gRPC 翻译。
type CollectionAdminHandler struct {
	client videov1.VideoServiceClient
}

// NewCollectionAdminHandler 构造 CollectionAdminHandler。
func NewCollectionAdminHandler(client videov1.VideoServiceClient) *CollectionAdminHandler {
	return &CollectionAdminHandler{client: client}
}

// AdminList 分页查询全部合集（含草稿）：GET /api/v1/admin/collections
func (h *CollectionAdminHandler) AdminList(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	resp, err := h.client.AdminListCollections(c.Request.Context(), &videov1.AdminListCollectionsRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	list := make([]dto.CollectionInfo, 0, len(resp.GetCollections()))
	for _, v := range resp.GetCollections() {
		list = append(list, toCollectionInfo(v))
	}
	response.OK(c, collectionPage{
		Items:    list,
		Total:    resp.GetTotal(),
		Page:     int(resp.GetPage()),
		PageSize: int(resp.GetPageSize()),
	})
}

// AdminDetail 查询合集详情（含草稿与未上架视频）：GET /api/v1/admin/collections/{id}
//
// 编排面板必须看到合集内全部视频的真实顺序，公开详情接口会过滤，
// 所以管理端单独走这个接口。
func (h *CollectionAdminHandler) AdminDetail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的合集 ID")
		return
	}

	resp, err := h.client.AdminGetCollection(c.Request.Context(), &videov1.AdminGetCollectionRequest{Id: id})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	videos := make([]dto.VideoInfo, 0, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		videos = append(videos, toVideoInfo(v))
	}
	response.OK(c, dto.CollectionDetail{
		CollectionInfo: toCollectionInfo(resp.GetCollection()),
		Videos:         videos,
	})
}

// Create 新建合集：POST /api/v1/admin/collections
//
// 请求体 {"title":"...","description":"...","cover_url":"..."}；
// 创建者身份由 gRPC metadata 透传，不放在请求体里。
func (h *CollectionAdminHandler) Create(c *gin.Context) {
	var in dto.CreateCollectionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "请求体格式错误")
		return
	}

	resp, err := h.client.CreateCollection(c.Request.Context(), &videov1.CreateCollectionRequest{
		Title:       in.Title,
		Description: in.Description,
		CoverUrl:    in.CoverURL,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toCollectionInfo(resp.GetCollection()))
}

// Update 编辑合集：PUT /api/v1/admin/collections/{id}
//
// 请求体字段均可选，传了才改：title / description / cover_url（传空串表示清空，
// 封面清空后读侧重新用第一个视频封面兜底）/ status（0 草稿、1 公开）。
func (h *CollectionAdminHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的合集 ID")
		return
	}

	var in dto.UpdateCollectionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "请求体格式错误")
		return
	}

	req := &videov1.UpdateCollectionRequest{
		Id:          id,
		Title:       in.Title,
		Description: in.Description,
		CoverUrl:    in.CoverURL,
	}
	if in.Status != nil {
		status := int32(*in.Status)
		req.Status = &status
	}

	resp, err := h.client.UpdateCollection(c.Request.Context(), req)
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toCollectionInfo(resp.GetCollection()))
}

// Delete 删除合集：DELETE /api/v1/admin/collections/{id}
//
// 只删合集与关联，不删视频本身；雪花 ID 以字符串回传。
func (h *CollectionAdminHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的合集 ID")
		return
	}

	resp, err := h.client.DeleteCollection(c.Request.Context(), &videov1.DeleteCollectionRequest{Id: id})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, gin.H{"id": resp.GetId()})
}

// AddVideos 批量加视频：POST /api/v1/admin/collections/{id}/videos
//
// 请求体 {"video_ids":["...","..."]}，按数组顺序追加到合集末尾；
// 已在合集内的视频会被忽略（幂等）。
func (h *CollectionAdminHandler) AddVideos(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的合集 ID")
		return
	}

	videoIDs, ok := parseCollectionVideoIDs(c)
	if !ok {
		return
	}

	resp, err := h.client.AddCollectionVideos(c.Request.Context(), &videov1.AddCollectionVideosRequest{
		CollectionId: id,
		VideoIds:     videoIDs,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toCollectionInfo(resp.GetCollection()))
}

// RemoveVideo 移出视频：DELETE /api/v1/admin/collections/{id}/videos/{videoId}
func (h *CollectionAdminHandler) RemoveVideo(c *gin.Context) {
	collectionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || collectionID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的合集 ID")
		return
	}
	videoID, err := strconv.ParseInt(c.Param("videoId"), 10, 64)
	if err != nil || videoID <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
		return
	}

	resp, err := h.client.RemoveCollectionVideo(c.Request.Context(), &videov1.RemoveCollectionVideoRequest{
		CollectionId: collectionID,
		VideoId:      videoID,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toCollectionInfo(resp.GetCollection()))
}

// Reorder 重排播放顺序：PATCH /api/v1/admin/collections/{id}/order
//
// 请求体 {"video_ids":[...]} 必须是合集内现有视频的完整顺序，
// 集合不一致时服务端返回参数错误——避免「漏传的视频」意外落到末尾。
func (h *CollectionAdminHandler) Reorder(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的合集 ID")
		return
	}

	videoIDs, ok := parseCollectionVideoIDs(c)
	if !ok {
		return
	}

	resp, err := h.client.ReorderCollectionVideos(c.Request.Context(), &videov1.ReorderCollectionVideosRequest{
		CollectionId: id,
		VideoIds:     videoIDs,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, toCollectionInfo(resp.GetCollection()))
}

// parseCollectionVideoIDs 解析请求体里的视频 ID 列表（字符串形式，规避 JS 精度问题）。
//
// 空列表直接判参数错误：追加空列表是空操作，重排空列表则无法表达「清空合集」
// （移出视频有专门的 DELETE 接口），两种都更像前端状态异常而非合法意图。
func parseCollectionVideoIDs(c *gin.Context) ([]int64, bool) {
	var in dto.CollectionVideoIDsInput
	if err := c.ShouldBindJSON(&in); err != nil || len(in.VideoIDs) == 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "video_ids 不能为空")
		return nil, false
	}

	ids := make([]int64, 0, len(in.VideoIDs))
	for _, raw := range in.VideoIDs {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的视频 ID")
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// toCollectionInfo 把跨服务契约的合集信息映射回对外 DTO。
func toCollectionInfo(c *videov1.CollectionInfo) dto.CollectionInfo {
	info := dto.CollectionInfo{
		ID:          c.GetId(),
		Title:       c.GetTitle(),
		Description: c.GetDescription(),
		CoverURL:    c.GetCoverUrl(),
		UploaderID:  c.GetUploaderId(),
		Status:      int8(c.GetStatus()),
		VideoCount:  c.GetVideoCount(),
	}
	// created_at 经 gRPC 传为 UTC 时间戳，转回本地时区，序列化结果与进程内直读数据库一致。
	if ts := c.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}

// toCollectionBrief 把跨服务契约的合集精简信息映射回对外 DTO。
func toCollectionBrief(b *videov1.CollectionBrief) dto.CollectionBrief {
	return dto.CollectionBrief{
		ID:           b.GetId(),
		Title:        b.GetTitle(),
		CoverURL:     b.GetCoverUrl(),
		VideoCount:   b.GetVideoCount(),
		CurrentIndex: int(b.GetCurrentIndex()),
	}
}
