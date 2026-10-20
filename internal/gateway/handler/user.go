package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/middleware"
	"github.com/sobremesatime/chuangyue/pkg/response"
)

// UserHandler 是用户资料自我管理接口的网关处理器，经 gRPC 客户端访问用户服务。
type UserHandler struct {
	client userv1.UserServiceClient
}

// NewUserHandler 构造 UserHandler。
func NewUserHandler(client userv1.UserServiceClient) *UserHandler {
	return &UserHandler{client: client}
}

// Profile 获取当前登录用户资料：GET /api/v1/users/me
func (h *UserHandler) Profile(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	resp, err := h.client.GetProfile(c.Request.Context(), &userv1.GetProfileRequest{})
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	response.OK(c, toUserInfoDTO(resp.GetUser()))
}

// UpdateProfile 更新当前登录用户资料：PATCH /api/v1/users/me
//
// 部分更新：请求体里没出现的字段不动（指针为 nil 即不放进契约）；详细校验在服务端完成。
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	var req dto.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, err.Error())
		return
	}

	in := &userv1.UpdateProfileRequest{
		Username:  req.Username,
		Nickname:  req.Nickname,
		Signature: req.Signature,
		Birthday:  req.Birthday,
		School:    req.School,
		Location:  req.Location,
	}
	if req.Gender != nil {
		gender := int32(*req.Gender)
		in.Gender = &gender
	}

	resp, err := h.client.UpdateProfile(c.Request.Context(), in)
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	response.OK(c, toUserInfoDTO(resp.GetUser()))
}

// ChangePassword 修改当前登录用户密码：PUT /api/v1/users/me/password
func (h *UserHandler) ChangePassword(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	var req dto.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "请输入当前密码和新密码")
		return
	}

	resp, err := h.client.ChangePassword(c.Request.Context(), &userv1.ChangePasswordRequest{
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
	})
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	response.OK(c, toUserInfoDTO(resp.GetUser()))
}

// UploadAvatar 上传 / 更换当前用户头像：POST /api/v1/users/me/avatar
//
// multipart/form-data，字段 file（必填）。头像字节经客户端流式转发给用户服务；
// 返回更新过的用户资料，前端据此就地刷新头像，无需再发一次 /users/me。
func (h *UserHandler) UploadAvatar(c *gin.Context) {
	if _, ok := middleware.UserID(c); !ok {
		response.Fail(c, errcode.ErrUnauthorized)
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.FailWithMsg(c, errcode.ErrInvalidParam, "缺少头像文件")
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		response.Fail(c, errcode.ErrUploadFailed)
		return
	}
	defer file.Close()

	stream, err := h.client.UploadAvatar(c.Request.Context())
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	meta := &userv1.UploadAvatarMeta{
		Filename: fileHeader.Filename,
		Size:     fileHeader.Size,
	}
	if err := stream.Send(&userv1.UploadAvatarFrame{Payload: &userv1.UploadAvatarFrame_Meta{Meta: meta}}); err != nil {
		failFromGRPCUser(c, err)
		return
	}
	if err := sendChunks(func(chunk []byte) error {
		return stream.Send(&userv1.UploadAvatarFrame{Payload: &userv1.UploadAvatarFrame_Chunk{Chunk: chunk}})
	}, file); err != nil {
		failFromGRPCUser(c, err)
		return
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		failFromGRPCUser(c, err)
		return
	}
	response.OK(c, toUserInfoDTO(resp.GetUser()))
}

// failFromGRPCUser 把用户服务的 gRPC 错误映射为对外响应。
//
// 优先用服务端编码进 status.message 的业务错误码原样还原，保证 HTTP 状态码与响应 code
// 与改造前逐字一致；旧的纯 NotFound 语义走兜底，其余按系统异常统一 500。
//
// 刻意与视频域的 failFromGRPC 分开：视频域把 NotFound 兜底为「视频不存在」，
// 这里必须兜底为「用户不存在」，避免串味。
func failFromGRPCUser(c *gin.Context, err error) {
	if ec, ok := grpcerr.Unwrap(err); ok {
		response.FailWithMsg(c, ec, ec.Message)
		return
	}
	if status.Code(err) == codes.NotFound {
		response.Fail(c, errcode.ErrUserNotFound)
		return
	}
	response.FailErr(c, err)
}

// toUserInfoDTO 把跨服务契约的完整用户信息映射回对外 DTO，序列化口径（含 id,string）保持不变。
func toUserInfoDTO(u *userv1.UserInfo) dto.UserInfo {
	info := dto.UserInfo{
		ID:        u.GetId(),
		Username:  u.GetUsername(),
		Email:     u.GetEmail(),
		Nickname:  u.GetNickname(),
		Avatar:    u.GetAvatar(),
		Signature: u.GetSignature(),
		Gender:    int8(u.GetGender()),
		Birthday:  u.GetBirthday(),
		Age:       int(u.GetAge()),
		School:    u.GetSchool(),
		Location:  u.GetLocation(),
		Role:      int8(u.GetRole()),
		Status:    int8(u.GetStatus()),
	}
	// created_at 经 gRPC 传为 UTC 时间戳，转回本地时区，序列化结果与进程内直读数据库一致。
	if ts := u.GetCreatedAt(); ts != nil {
		info.CreatedAt = ts.AsTime().In(time.Local)
	}
	return info
}
