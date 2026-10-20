// Package errcode 定义全局业务错误码。
//
// 约定：
//   - code = 0 表示成功；
//   - 错误码按业务域分段，便于快速定位来源：
//   - 1xxxx 通用 / 框架层
//   - 2xxxx 用户域
//   - 3xxxx 视频域
//
// 每个错误码同时携带对用户友好的中文提示，由响应层统一返回。
package errcode

import "fmt"

// ErrCode 是一个业务错误码，实现了 error 接口，可直接作为 error 使用。
type ErrCode struct {
	Code    int    // 业务错误码
	Message string // 面向用户的提示信息
}

// Error 实现 error 接口，便于在服务层直接 return errcode.ErrUserNotFound。
func (e ErrCode) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// New 创建一个新的错误码，供各业务域扩展使用。
func New(code int, message string) ErrCode {
	return ErrCode{Code: code, Message: message}
}

// WithMsg 返回一个仅替换提示信息、保留错误码的新错误码。
func (e ErrCode) WithMsg(msg string) ErrCode {
	return ErrCode{Code: e.Code, Message: msg}
}

// 通用 / 框架层错误码（1xxxx）。
var (
	Success         = ErrCode{Code: 0, Message: "成功"}
	ErrInvalidParam = ErrCode{Code: 10001, Message: "请求参数错误"}
	ErrUnauthorized = ErrCode{Code: 10002, Message: "未登录或登录已过期"}
	ErrForbidden    = ErrCode{Code: 10003, Message: "没有操作权限"}
	ErrNotFound     = ErrCode{Code: 10004, Message: "资源不存在"}
	ErrTooManyReq   = ErrCode{Code: 10005, Message: "请求过于频繁，请稍后再试"}
	ErrServerBusy   = ErrCode{Code: 10006, Message: "服务繁忙，请稍后再试"}
	ErrInternal     = ErrCode{Code: 10007, Message: "服务器内部错误"}
)

// 用户域错误码（2xxxx）。
var (
	ErrUsernameTaken       = ErrCode{Code: 20001, Message: "用户名已被占用"}
	ErrInvalidLogin        = ErrCode{Code: 20002, Message: "用户名或密码错误"}
	ErrUserNotFound        = ErrCode{Code: 20003, Message: "用户不存在"}
	ErrTokenInvalid        = ErrCode{Code: 20004, Message: "令牌无效或已过期"}
	ErrUserDisabled        = ErrCode{Code: 20005, Message: "账号已被禁用"}
	ErrPasswordMismatch    = ErrCode{Code: 20006, Message: "两次输入的密码不一致"}
	ErrEmailTaken          = ErrCode{Code: 20007, Message: "该邮箱已被注册"}
	ErrCodeInvalid         = ErrCode{Code: 20008, Message: "验证码错误或已过期"}
	ErrCodeTooManyAttempts = ErrCode{Code: 20009, Message: "验证码错误次数过多，请重新获取"}
	ErrOldPasswordWrong    = ErrCode{Code: 20010, Message: "当前密码不正确"}
)

// 注意：限流场景一律复用通用域的 ErrTooManyReq(10005)。
// pkg/response 只把它映射为 HTTP 429，新增的 2xxxx 码会被 default 分支退回 HTTP 200。

// 视频域错误码（3xxxx）。
var (
	ErrVideoNotFound         = ErrCode{Code: 30001, Message: "视频不存在"}
	ErrVideoOffline          = ErrCode{Code: 30002, Message: "视频已下架"}
	ErrFileTypeDenied        = ErrCode{Code: 30003, Message: "不支持的文件类型"}
	ErrFileTooLarge          = ErrCode{Code: 30004, Message: "文件超出大小限制"}
	ErrUploadFailed          = ErrCode{Code: 30005, Message: "文件上传失败"}
	ErrCommentNotFound       = ErrCode{Code: 30006, Message: "评论不存在"}
	ErrDanmakuNotFound       = ErrCode{Code: 30007, Message: "弹幕不存在"}
	ErrReportNotFound        = ErrCode{Code: 30008, Message: "举报不存在"}
	ErrReportDuplicated      = ErrCode{Code: 30009, Message: "你已举报过该内容"}
	ErrUploadSessionNotFound = ErrCode{Code: 30010, Message: "上传会话不存在或已失效"}
	ErrUploadInvalidPart     = ErrCode{Code: 30011, Message: "分片序号或大小不合法"}
	ErrUploadNotComplete     = ErrCode{Code: 30012, Message: "分片尚未全部上传完成"}
	ErrStorageUnsupported    = ErrCode{Code: 30013, Message: "当前存储驱动不支持该操作"}
)
