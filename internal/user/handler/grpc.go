// Package handler 暴露用户域的 gRPC 接口。
package handler

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	userv1 "github.com/sobremesatime/chuangyue/api/gen/user/v1"
	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/internal/user/model"
	"github.com/sobremesatime/chuangyue/internal/user/service"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// errFrameFormat 表示客户端在元信息帧之后又发来非数据帧，属协议错误。
var errFrameFormat = grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("数据帧格式错误"))

// ============================================================
// UserService：跨服务展示信息 + 账号自我管理
// ============================================================

// GRPCUserServer 是用户域 gRPC 契约的服务端实现。
//
// 内嵌 Unimplemented 以满足前向兼容，未实现的方法会返回 Unimplemented。
type GRPCUserServer struct {
	userv1.UnimplementedUserServiceServer
	svc *service.Service
}

// NewGRPCUserServer 构造 GRPCUserServer。
//
// 依赖 *service.Service 即可覆盖整个用户域：Service 内嵌 QueryService，
// 跨服务读路径与账号写路径共用同一个仓储。
func NewGRPCUserServer(svc *service.Service) *GRPCUserServer {
	return &GRPCUserServer{svc: svc}
}

// GetUsers 批量返回用户简要信息，完成 model 到 proto 的映射。
//
// 契约语义是「存在才返回」：不存在的 id 直接跳过，空结果返回空列表而非 NotFound，
// 这样调用方拼出含失效 id 的批量请求时不会整批失败。
func (s *GRPCUserServer) GetUsers(ctx context.Context, req *userv1.GetUsersRequest) (*userv1.GetUsersResponse, error) {
	users, err := s.svc.GetByIDs(ctx, req.GetIds())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "批量查询用户失败: %v", err)
	}

	resp := &userv1.GetUsersResponse{Users: make([]*userv1.UserBrief, 0, len(users))}
	for i := range users {
		resp.Users = append(resp.Users, toUserBrief(&users[i]))
	}
	return resp, nil
}

// GetProfile 返回单个用户的完整资料。
func (s *GRPCUserServer) GetProfile(ctx context.Context, req *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	info, err := s.svc.Profile(ctx, userID)
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &userv1.GetProfileResponse{User: toProtoUserInfo(info)}, nil
}

// UpdateProfile 部分更新个人资料，返回更新后的完整资料。
//
// 用 optional 字段的「是否为 nil」还原 dto 的指针语义：缺失表示不修改，
// 传空串表示清空；带中文提示的字段校验仍在 service 层完成。
func (s *GRPCUserServer) UpdateProfile(ctx context.Context, req *userv1.UpdateProfileRequest) (*userv1.UpdateProfileResponse, error) {
	in := dto.UpdateProfileRequest{
		Username:  req.Username,
		Nickname:  req.Nickname,
		Signature: req.Signature,
		Birthday:  req.Birthday,
		School:    req.School,
		Location:  req.Location,
	}
	if req.Gender != nil {
		gender := int8(*req.Gender)
		in.Gender = &gender
	}

	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}

	info, err := s.svc.UpdateProfile(ctx, userID, in)
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &userv1.UpdateProfileResponse{User: toProtoUserInfo(info)}, nil
}

// ChangePassword 修改密码（需校验当前密码），返回更新后的完整资料。
func (s *GRPCUserServer) ChangePassword(ctx context.Context, req *userv1.ChangePasswordRequest) (*userv1.ChangePasswordResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	info, err := s.svc.ChangePassword(ctx, userID, dto.ChangePasswordRequest{
		OldPassword: req.GetOldPassword(),
		NewPassword: req.GetNewPassword(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &userv1.ChangePasswordResponse{User: toProtoUserInfo(info)}, nil
}

// UploadAvatar 上传 / 更换头像（客户端流式）。
//
// 首帧为元信息，其后各帧是头像字节；字节经管道交给 Service 落盘，
// 首帧到达前不触碰存储，避免流早退留下无主文件。
func (s *GRPCUserServer) UploadAvatar(stream grpc.ClientStreamingServer[userv1.UploadAvatarFrame, userv1.UploadAvatarResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("缺少头像元信息"))
		}
		return err
	}
	meta := first.GetMeta()
	if meta == nil {
		return grpcerr.Wrap(errcode.ErrInvalidParam.WithMsg("首帧必须是头像元信息"))
	}

	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(stream.Context())
	if userID <= 0 {
		return grpcerr.Wrap(errcode.ErrUnauthorized)
	}

	fileR, fileW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- pumpChunks(fileW, recvAvatarChunk(stream)) }()

	info, err := s.svc.UploadAvatar(stream.Context(), userID, dto.UploadAvatarInput{
		Filename: meta.GetFilename(),
		Size:     meta.GetSize(),
		File:     fileR,
	})
	// 关闭读端以解除可能阻塞的写入，再等泵协程收尾，避免 goroutine 泄漏。
	_ = fileR.CloseWithError(io.ErrClosedPipe)
	<-done

	if err != nil {
		return grpcerr.Wrap(err)
	}
	return stream.SendAndClose(&userv1.UploadAvatarResponse{User: toProtoUserInfo(info)})
}

// ============================================================
// 管理端：用户管理
// ============================================================

// AdminListUsers 分页查询全部用户，支持关键词与角色 / 状态过滤。
//
// 鉴权在网关的 admin 分组（Auth + RequireRole），这里不再重复判角色——
// 与视频域的管理端 RPC 口径一致。
func (s *GRPCUserServer) AdminListUsers(ctx context.Context, req *userv1.AdminListUsersRequest) (*userv1.AdminListUsersResponse, error) {
	in := dto.AdminListUsersRequest{
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
		Keyword:  req.GetKeyword(),
	}
	if req.Role != nil {
		role := int8(*req.Role)
		in.Role = &role
	}
	if req.Status != nil {
		status := int8(*req.Status)
		in.Status = &status
	}

	page, err := s.svc.AdminList(ctx, in)
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	resp := &userv1.AdminListUsersResponse{
		Users:    make([]*userv1.UserInfo, 0, len(page.Users)),
		Total:    page.Total,
		Page:     int32(page.Page),
		PageSize: int32(page.PageSize),
	}
	for i := range page.Users {
		resp.Users = append(resp.Users, toProtoUserInfo(&page.Users[i]))
	}
	return resp, nil
}

// AdminUpdateUser 修改指定用户的账号状态或角色（不允许改自己）。
func (s *GRPCUserServer) AdminUpdateUser(ctx context.Context, req *userv1.AdminUpdateUserRequest) (*userv1.AdminUpdateUserResponse, error) {
	// 操作者身份来自 gRPC metadata：服务端要用它拦住「管理员改自己」。
	operatorID := identity.UserID(ctx)
	if operatorID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}

	in := dto.AdminUpdateUserRequest{}
	if req.Status != nil {
		status := int8(*req.Status)
		in.Status = &status
	}
	if req.Role != nil {
		role := int8(*req.Role)
		in.Role = &role
	}

	info, err := s.svc.AdminUpdate(ctx, operatorID, req.GetId(), in)
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &userv1.AdminUpdateUserResponse{User: toProtoUserInfo(info)}, nil
}

// ============================================================
// AuthService：身份建立与令牌签发
// ============================================================

// GRPCAuthServer 是用户域认证契约的服务端实现。
type GRPCAuthServer struct {
	userv1.UnimplementedAuthServiceServer
	svc *service.Service
}

// NewGRPCAuthServer 构造 GRPCAuthServer。
func NewGRPCAuthServer(svc *service.Service) *GRPCAuthServer {
	return &GRPCAuthServer{svc: svc}
}

// SendCode 按用途（register / reset）生成验证码并投递到用户邮箱。
func (s *GRPCAuthServer) SendCode(ctx context.Context, req *userv1.SendCodeRequest) (*userv1.SendCodeResponse, error) {
	out, err := s.svc.SendCode(ctx, req.GetEmail(), req.GetIp(), req.GetPurpose())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &userv1.SendCodeResponse{
		ExpiresIn: out.ExpiresIn,
		Cooldown:  out.Cooldown,
	}, nil
}

// Register 校验验证码并注册新用户。
func (s *GRPCAuthServer) Register(ctx context.Context, req *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
	info, err := s.svc.Register(ctx, dto.RegisterRequest{
		Email:    req.GetEmail(),
		Code:     req.GetCode(),
		Password: req.GetPassword(),
		Nickname: req.GetNickname(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &userv1.RegisterResponse{User: toProtoUserInfo(info)}, nil
}

// Login 校验账号密码并签发令牌。
//
// 刷新令牌与刷新有效期一并回给网关，由网关写入 HttpOnly Cookie。
func (s *GRPCAuthServer) Login(ctx context.Context, req *userv1.LoginRequest) (*userv1.LoginResponse, error) {
	_, pair, refreshToken, err := s.svc.Login(ctx, dto.LoginRequest{
		Account:  req.GetAccount(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return toLoginResponse(pair, refreshToken, s.svc.RefreshExpire()), nil
}

// Refresh 用刷新令牌换取新的访问令牌，并轮换刷新令牌。
func (s *GRPCAuthServer) Refresh(ctx context.Context, req *userv1.RefreshRequest) (*userv1.RefreshResponse, error) {
	pair, newRefresh, err := s.svc.Refresh(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	// Refresh 与 Login 的响应结构字段一致，复用同一份构造。
	return &userv1.RefreshResponse{
		AccessToken:      pair.AccessToken,
		TokenType:        pair.TokenType,
		ExpiresIn:        pair.ExpiresIn,
		RefreshToken:     newRefresh,
		RefreshExpiresIn: int64(s.svc.RefreshExpire().Seconds()),
	}, nil
}

// ResetPassword 用邮箱验证码重置密码（匿名可调用）。
//
// 刻意不回令牌：重置会让该用户此前签发的刷新令牌全部失效，
// 接着就发一枚新的等于把刚吊销的东西又还回去；用户需要用新密码重新登录。
func (s *GRPCAuthServer) ResetPassword(ctx context.Context, req *userv1.ResetPasswordRequest) (*userv1.ResetPasswordResponse, error) {
	err := s.svc.ResetPassword(ctx, dto.ResetPasswordRequest{
		Email:       req.GetEmail(),
		Code:        req.GetCode(),
		NewPassword: req.GetNewPassword(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &userv1.ResetPasswordResponse{}, nil
}

// ============================================================
// 流式泵送与契约映射
// ============================================================

// pumpChunks 从 recv 逐帧读取字节写入管道，直到读到 EOF。
//
// 无论正常结束还是出错都会关闭写端，向 Service 的读端发出结束信号。
func pumpChunks(w *io.PipeWriter, recv func() ([]byte, error)) error {
	for {
		chunk, err := recv()
		if errors.Is(err, io.EOF) {
			_ = w.Close()
			return nil
		}
		if err != nil {
			_ = w.CloseWithError(err)
			return err
		}
		if len(chunk) == 0 {
			continue
		}
		if _, err := w.Write(chunk); err != nil {
			_ = w.CloseWithError(err)
			return err
		}
	}
}

// recvAvatarChunk 把 UploadAvatar 的后续帧规整为「字节 + error」，非数据帧按协议错误处理。
func recvAvatarChunk(stream grpc.ClientStreamingServer[userv1.UploadAvatarFrame, userv1.UploadAvatarResponse]) func() ([]byte, error) {
	return func() ([]byte, error) {
		f, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if chunk := f.GetChunk(); chunk != nil {
			return chunk, nil
		}
		return nil, errFrameFormat
	}
}

// toLoginResponse 把令牌对与刷新令牌映射为登录 / 刷新响应。
func toLoginResponse(pair *dto.TokenPair, refreshToken string, refreshExpire time.Duration) *userv1.LoginResponse {
	return &userv1.LoginResponse{
		AccessToken:      pair.AccessToken,
		TokenType:        pair.TokenType,
		ExpiresIn:        pair.ExpiresIn,
		RefreshToken:     refreshToken,
		RefreshExpiresIn: int64(refreshExpire.Seconds()),
	}
}

// toProtoUserInfo 把对外 DTO 映射为完整用户契约。
//
// created_at 用时间戳承载，网关侧再转回本地时区，序列化口径与改造前一致。
func toProtoUserInfo(u *dto.UserInfo) *userv1.UserInfo {
	return &userv1.UserInfo{
		Id:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Nickname:  u.Nickname,
		Avatar:    u.Avatar,
		Signature: u.Signature,
		Gender:    int32(u.Gender),
		Birthday:  u.Birthday,
		Age:       int32(u.Age),
		School:    u.School,
		Location:  u.Location,
		Role:      int32(u.Role),
		Status:    int32(u.Status),
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
}

// toUserBrief 把用户模型映射为跨服务契约的简要信息，不暴露邮箱、角色等内部字段。
func toUserBrief(u *model.User) *userv1.UserBrief {
	return &userv1.UserBrief{
		Id:       u.ID,
		Username: u.Username,
		Nickname: u.Nickname,
		Avatar:   u.Avatar,
	}
}
