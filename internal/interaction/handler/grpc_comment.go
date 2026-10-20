// Package handler 暴露互动域评论能力的 gRPC 服务端实现。
package handler

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	"github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/grpcerr"
	"github.com/sobremesatime/chuangyue/pkg/identity"
)

// GRPCCommentServer 是互动域评论 gRPC 契约的服务端实现。
//
// 只做 proto 与 service 层之间的字段映射，业务逻辑全部委托给 CommentService。
type GRPCCommentServer struct {
	interactionv1.UnimplementedCommentServiceServer
	comments *service.CommentService
}

// NewGRPCCommentServer 构造 GRPCCommentServer。
func NewGRPCCommentServer(comments *service.CommentService) *GRPCCommentServer {
	return &GRPCCommentServer{comments: comments}
}

// ListComments 分页查询某视频下的一级评论（含内嵌回复预览）。
func (s *GRPCCommentServer) ListComments(ctx context.Context, req *interactionv1.ListCommentsRequest) (*interactionv1.ListCommentsResponse, error) {
	// 查看者身份来自 gRPC metadata 透传；未登录为 0，是合法值，不报错
	list, total, p, ps, err := s.comments.List(ctx, req.GetVideoId(), identity.UserID(ctx), int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	comments := make([]*interactionv1.CommentInfo, 0, len(list))
	for i := range list {
		comments = append(comments, toProtoCommentInfo(&list[i]))
	}
	return &interactionv1.ListCommentsResponse{
		Comments: comments,
		Total:    total,
		Page:     int32(p),
		PageSize: int32(ps),
	}, nil
}

// ListReplies 分页查询某条一级评论下的回复。
func (s *GRPCCommentServer) ListReplies(ctx context.Context, req *interactionv1.ListRepliesRequest) (*interactionv1.ListRepliesResponse, error) {
	// 查看者身份来自 gRPC metadata 透传；未登录为 0，是合法值，不报错
	list, total, p, ps, err := s.comments.ListReplies(ctx, req.GetParentId(), identity.UserID(ctx), int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	comments := make([]*interactionv1.CommentInfo, 0, len(list))
	for i := range list {
		comments = append(comments, toProtoCommentInfo(&list[i]))
	}
	return &interactionv1.ListRepliesResponse{
		Comments: comments,
		Total:    total,
		Page:     int32(p),
		PageSize: int32(ps),
	}, nil
}

// CreateComment 发表评论或回复。
func (s *GRPCCommentServer) CreateComment(ctx context.Context, req *interactionv1.CreateCommentRequest) (*interactionv1.CreateCommentResponse, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	info, err := s.comments.Create(ctx, userID, req.GetVideoId(), req.GetParentId(), req.GetContent())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.CreateCommentResponse{Comment: toProtoCommentInfo(info)}, nil
}

// DeleteComment 删除评论（仅作者本人或管理员）。
func (s *GRPCCommentServer) DeleteComment(ctx context.Context, req *interactionv1.DeleteCommentRequest) (*interactionv1.DeleteCommentResponse, error) {
	// 操作者身份与角色来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	if err := s.comments.Delete(ctx, req.GetCommentId(), userID, identity.IsAdmin(ctx)); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.DeleteCommentResponse{}, nil
}

// LikeComment 点赞评论。
func (s *GRPCCommentServer) LikeComment(ctx context.Context, req *interactionv1.LikeCommentRequest) (*interactionv1.LikeResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.comments.Like(ctx, userID, req.GetCommentId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.LikeResult{Liked: result.Liked, LikeCount: result.LikeCount}, nil
}

// UnlikeComment 取消点赞评论。
func (s *GRPCCommentServer) UnlikeComment(ctx context.Context, req *interactionv1.UnlikeCommentRequest) (*interactionv1.LikeResult, error) {
	// 身份来自 gRPC metadata 透传，未登录直接拒绝
	userID := identity.UserID(ctx)
	if userID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	result, err := s.comments.Unlike(ctx, userID, req.GetCommentId())
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}
	return &interactionv1.LikeResult{Liked: result.Liked, LikeCount: result.LikeCount}, nil
}

// AdminListComments 分页查询全部正常评论，供管理端内容治理。
func (s *GRPCCommentServer) AdminListComments(ctx context.Context, req *interactionv1.AdminListCommentsRequest) (*interactionv1.AdminListCommentsResponse, error) {
	list, total, p, ps, err := s.comments.AdminList(ctx, videodto.AdminContentQuery{
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
		Keyword:  req.GetKeyword(),
	})
	if err != nil {
		return nil, grpcerr.Wrap(err)
	}

	comments := make([]*interactionv1.AdminCommentInfo, 0, len(list))
	for i := range list {
		comments = append(comments, toProtoAdminCommentInfo(&list[i]))
	}
	return &interactionv1.AdminListCommentsResponse{
		Comments: comments,
		Total:    total,
		Page:     int32(p),
		PageSize: int32(ps),
	}, nil
}

// AdminDeleteComment 管理端删除评论。
func (s *GRPCCommentServer) AdminDeleteComment(ctx context.Context, req *interactionv1.AdminDeleteCommentRequest) (*interactionv1.AdminDeleteCommentResponse, error) {
	// 操作者身份来自 metadata：删完要按它给作者写「内容被删除」通知（自己删自己不提醒）。
	operatorID := identity.UserID(ctx)
	if operatorID <= 0 {
		return nil, grpcerr.Wrap(errcode.ErrUnauthorized)
	}
	if err := s.comments.AdminDelete(ctx, operatorID, req.GetCommentId()); err != nil {
		return nil, grpcerr.Wrap(err)
	}
	// 雪花 ID 以字符串回传的实现由网关完成，这里只把 id 原样带回
	return &interactionv1.AdminDeleteCommentResponse{Id: req.GetCommentId()}, nil
}

// toProtoCommentInfo 把评论 DTO 映射为跨服务契约结构。
//
// Replies 始终给出非 nil 切片，保证网关侧即使无回复也序列化成 [] 而非 null。
func toProtoCommentInfo(c *dto.CommentInfo) *interactionv1.CommentInfo {
	replies := make([]*interactionv1.CommentInfo, 0, len(c.Replies))
	for i := range c.Replies {
		replies = append(replies, toProtoCommentInfo(&c.Replies[i]))
	}

	info := &interactionv1.CommentInfo{
		Id:              c.ID,
		VideoId:         c.VideoID,
		UserId:          c.UserID,
		Username:        c.Username,
		Nickname:        c.Nickname,
		Avatar:          c.Avatar,
		Content:         c.Content,
		LikeCount:       c.LikeCount,
		Liked:           c.Liked,
		ParentId:        c.ParentID,
		ReplyToUserId:   c.ReplyToUserID,
		ReplyToNickname: c.ReplyToNickname,
		ReplyCount:      c.ReplyCount,
		Replies:         replies,
	}
	if !c.CreatedAt.IsZero() {
		info.CreatedAt = timestamppb.New(c.CreatedAt)
	}
	return info
}

// toProtoAdminCommentInfo 把管理端评论 DTO 映射为跨服务契约结构。
func toProtoAdminCommentInfo(c *videodto.AdminCommentInfo) *interactionv1.AdminCommentInfo {
	info := &interactionv1.AdminCommentInfo{
		Id:              c.ID,
		VideoId:         c.VideoID,
		VideoTitle:      c.VideoTitle,
		UserId:          c.UserID,
		Nickname:        c.Nickname,
		Avatar:          c.Avatar,
		Content:         c.Content,
		ParentId:        c.ParentID,
		ReplyToNickname: c.ReplyToNickname,
		ReplyCount:      c.ReplyCount,
		LikeCount:       c.LikeCount,
	}
	if !c.CreatedAt.IsZero() {
		info.CreatedAt = timestamppb.New(c.CreatedAt)
	}
	return info
}
