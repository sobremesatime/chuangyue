// Package grpcerr 在 gRPC 状态错误与业务错误码之间做双向映射。
//
// 视频域服务化后，服务端把 errcode.ErrCode 连同码值编码进 status 的 message，
// 网关按同一格式还原成对外错误码，保证 HTTP 状态码与响应 code 在改造前后逐字一致。
// 双方共用本包，避免「编码 / 解码」的字符串格式在两个进程里各写一份而漂移。
package grpcerr

import (
	"errors"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
)

// codeSep 分隔错误码与提示信息，出现在 status.message 中，形如 "30001|视频不存在"。
const codeSep = "|"

// Wrap 把服务层的 error 转成 gRPC 错误。
//
// 业务错误码（errcode.ErrCode）连同码值一起编码进 message，供网关原样还原；
// 其余错误（数据库 / 存储故障等）统一按 Internal 处理，原始信息保留在 message 里。
func Wrap(err error) error {
	var ec errcode.ErrCode
	if errors.As(err, &ec) {
		return status.Errorf(codeFor(ec.Code), "%d%s%s", ec.Code, codeSep, ec.Message)
	}
	return status.Errorf(codes.Internal, "服务器内部错误: %v", err)
}

// Unwrap 还原 Wrap 编码过的 gRPC 错误；非本包编码的错误返回 false。
func Unwrap(err error) (errcode.ErrCode, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return errcode.ErrCode{}, false
	}

	raw := st.Message()
	i := strings.Index(raw, codeSep)
	if i <= 0 {
		return errcode.ErrCode{}, false
	}
	code, convErr := strconv.Atoi(raw[:i])
	if convErr != nil {
		return errcode.ErrCode{}, false
	}
	return errcode.ErrCode{Code: code, Message: raw[i+len(codeSep):]}, true
}

// codeFor 把业务错误码映射为语义化的 gRPC 状态码。
//
// 网关优先用 message 里的码值还原对外响应，这里只决定 gRPC 层面的语义，
// 便于日志与按状态码分类的拦截器理解错误性质。
func codeFor(code int) codes.Code {
	switch code {
	case errcode.ErrNotFound.Code, errcode.ErrVideoNotFound.Code,
		errcode.ErrCommentNotFound.Code, errcode.ErrDanmakuNotFound.Code,
		errcode.ErrReportNotFound.Code, errcode.ErrUploadSessionNotFound.Code,
		errcode.ErrUserNotFound.Code:
		return codes.NotFound
	case errcode.ErrForbidden.Code, errcode.ErrUserDisabled.Code:
		return codes.PermissionDenied
	case errcode.ErrUnauthorized.Code, errcode.ErrTokenInvalid.Code,
		errcode.ErrInvalidLogin.Code, errcode.ErrOldPasswordWrong.Code:
		return codes.Unauthenticated
	case errcode.ErrReportDuplicated.Code,
		errcode.ErrUsernameTaken.Code, errcode.ErrEmailTaken.Code:
		return codes.AlreadyExists
	case errcode.ErrTooManyReq.Code:
		return codes.ResourceExhausted
	case errcode.ErrServerBusy.Code:
		return codes.Unavailable
	case errcode.ErrInternal.Code:
		return codes.Internal
	default:
		return codes.InvalidArgument
	}
}
