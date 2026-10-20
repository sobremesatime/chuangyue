// Package response 定义统一的 HTTP 返回格式。
//
// 所有接口（无论成功或失败）都返回同一个信封结构：
//
//	{ "code": 0, "message": "成功", "data": {...}, "trace_id": "xxx" }
//
// 前端只需判断 code 是否为 0，配合 trace_id 便于问题定位。
package response

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// TraceIDKey 是 gin.Context 中存放链路 ID 的键。
// 由 middleware.TraceID 写入，response 读取后回填到响应体。
const TraceIDKey = "trace_id"

// Body 是统一响应体。
type Body struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
}

// Page 是分页数据的通用载体。
type Page struct {
	List     any   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

// OK 返回成功响应，data 为业务数据。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{
		Code:    errcode.Success.Code,
		Message: errcode.Success.Message,
		Data:    data,
		TraceID: traceID(c),
	})
}

// OKPage 返回成功响应，data 为分页结构。
func OKPage(c *gin.Context, list any, total int64, page, pageSize int) {
	OK(c, Page{List: list, Total: total, Page: page, PageSize: pageSize})
}

// Fail 返回业务失败响应。HTTP 状态码由错误码映射得到。
func Fail(c *gin.Context, e errcode.ErrCode) {
	c.JSON(httpStatus(e.Code), Body{
		Code:    e.Code,
		Message: e.Message,
		TraceID: traceID(c),
	})
}

// FailWithMsg 返回业务失败响应，并覆盖默认提示信息。
func FailWithMsg(c *gin.Context, e errcode.ErrCode, msg string) {
	c.JSON(httpStatus(e.Code), Body{
		Code:    e.Code,
		Message: msg,
		TraceID: traceID(c),
	})
}

// Abort 用于中间件：返回失败响应并终止后续处理链。
func Abort(c *gin.Context, e errcode.ErrCode) {
	c.AbortWithStatusJSON(httpStatus(e.Code), Body{
		Code:    e.Code,
		Message: e.Message,
		TraceID: traceID(c),
	})
}

// FailErr 处理来自 service 层的 error：
//   - 若为业务错误码（errcode.ErrCode），按其码值返回；
//   - 否则视为系统异常，记录日志后统一返回 500，避免泄露内部细节。
func FailErr(c *gin.Context, err error) {
	var ec errcode.ErrCode
	if errors.As(err, &ec) {
		Fail(c, ec)
		return
	}

	logger.L().Error("请求处理失败",
		zap.Error(err),
		zap.String("path", c.Request.URL.Path),
		zap.String(TraceIDKey, traceID(c)),
	)
	Fail(c, errcode.ErrInternal)
}

// traceID 从上下文读取链路 ID，未设置时返回空串。
func traceID(c *gin.Context) string {
	return c.GetString(TraceIDKey)
}

// httpStatus 把业务错误码映射为语义化的 HTTP 状态码。
// 未特殊映射的业务错误统一返回 200，由 code 字段区分。
func httpStatus(code int) int {
	switch code {
	case errcode.Success.Code:
		return http.StatusOK
	case errcode.ErrUnauthorized.Code, errcode.ErrTokenInvalid.Code:
		return http.StatusUnauthorized
	case errcode.ErrForbidden.Code:
		return http.StatusForbidden
	case errcode.ErrNotFound.Code, errcode.ErrUserNotFound.Code, errcode.ErrVideoNotFound.Code:
		return http.StatusNotFound
	case errcode.ErrTooManyReq.Code:
		return http.StatusTooManyRequests
	case errcode.ErrServerBusy.Code:
		return http.StatusServiceUnavailable
	case errcode.ErrInternal.Code:
		return http.StatusInternalServerError
	default:
		return http.StatusOK
	}
}
