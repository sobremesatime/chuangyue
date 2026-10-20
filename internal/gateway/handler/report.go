package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// ReportHandler 是内容举报的网关处理器，经 gRPC 客户端访问视频服务。
type ReportHandler struct {
	client videov1.ReportServiceClient
}

// NewReportHandler 构造 ReportHandler。
func NewReportHandler(client videov1.ReportServiceClient) *ReportHandler {
	return &ReportHandler{client: client}
}

// Create 发起举报：POST /api/v1/reports
//
// 请求体 {"target_type":1|2,"target_id":"...","reason":"...","detail":"..."}，需要登录。
func (h *ReportHandler) Create(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	var in dto.CreateReportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "请求体格式错误")
		return
	}

	resp, err := h.client.CreateReport(c.Request.Context(), &videov1.CreateReportRequest{
		TargetType: int32(in.TargetType),
		TargetId:   in.TargetID,
		Reason:     in.Reason,
		Detail:     in.Detail,
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, reportInfoToDTO(resp.GetReport()))
}

// AdminList 分页查询举报列表：GET /api/v1/admin/reports
//
// status 查询参数可选，取值 0 待处理 / 1 已处理 / 2 已忽略，不传则返回全部。
func (h *ReportHandler) AdminList(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	status, err := parseStatusQuery(c.Query("status"))
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的举报状态")
		return
	}

	resp, err := h.client.ListReports(c.Request.Context(), &videov1.ListReportsRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
		Status:   toProtoInt32(status),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}

	list := make([]dto.ReportInfo, 0, len(resp.GetReports()))
	for _, r := range resp.GetReports() {
		list = append(list, reportInfoToDTO(r))
	}
	response.OKPage(c, list, resp.GetTotal(), int(resp.GetPage()), int(resp.GetPageSize()))
}

// Handle 处理举报：PATCH /api/v1/admin/reports/{id}
//
// 请求体 {"status": 1|2}，1 表示已处理，2 表示已忽略。
func (h *ReportHandler) Handle(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "非法的举报 ID")
		return
	}

	var in dto.HandleReportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "请求体格式错误")
		return
	}

	resp, err := h.client.HandleReport(c.Request.Context(), &videov1.HandleReportRequest{
		Id:     id,
		Status: int32(in.Status),
	})
	if err != nil {
		failFromGRPC(c, err)
		return
	}
	response.OK(c, reportInfoToDTO(resp.GetReport()))
}

// reportInfoToDTO 把跨服务契约的举报信息映射回对外 DTO。
//
// created_at / handled_at 经 gRPC 传为 UTC 时间戳，转回本地时区后序列化口径与进程内直读数据库一致。
func reportInfoToDTO(r *videov1.ReportInfo) dto.ReportInfo {
	out := dto.ReportInfo{
		ID:               r.GetId(),
		TargetType:       int8(r.GetTargetType()),
		TargetID:         r.GetTargetId(),
		VideoID:          r.GetVideoId(),
		VideoTitle:       r.GetVideoTitle(),
		ReporterID:       r.GetReporterId(),
		ReporterNickname: r.GetReporterNickname(),
		Reason:           r.GetReason(),
		Detail:           r.GetDetail(),
		Status:           int8(r.GetStatus()),
	}
	if ts := r.GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime().In(time.Local)
	}
	if ts := r.GetHandledAt(); ts != nil {
		handledAt := ts.AsTime().In(time.Local)
		out.HandledAt = &handledAt
	}
	return out
}
