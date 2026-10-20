package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/model"
	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// notificationSnapshotRunes 是通知里评论内容快照的最大字符数。
//
// 通知只负责「提示 + 预览」，太长没有意义，也避免超出建表时的 VARCHAR(255)。
const notificationSnapshotRunes = 80

// NotificationService 承载站内通知：列表、未读数、标记已读，以及供其他服务触发的写入。
type NotificationService struct {
	repo *repository.Repository
	// users 用于补全通知触发者的昵称 / 头像，替代原先仓储的 JOIN user
	users UserProvider
	// videos 用于补全通知行里的视频标题，替代原先仓储的 LEFT JOIN video
	videos VideoProvider
}

// NewNotificationService 构造 NotificationService。
//
// users / videos 分别为互动域取用户与视频信息的端口，由装配层注入。
func NewNotificationService(repo *repository.Repository, users UserProvider, videos VideoProvider) *NotificationService {
	return &NotificationService{repo: repo, users: users, videos: videos}
}

// List 分页查询当前用户收到的通知，按时间倒序。
func (s *NotificationService) List(ctx context.Context, userID int64, page, pageSize int) ([]dto.NotificationInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(page, pageSize)
	rows, total, err := s.repo.ListNotifications(ctx, userID, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	fillNotificationUsers(ctx, s.users, rows)
	fillNotificationVideoTitles(ctx, s.videos, rows)

	list := make([]dto.NotificationInfo, 0, len(rows))
	for i := range rows {
		list = append(list, toNotificationInfo(&rows[i]))
	}
	return list, total, p, ps, nil
}

// Unread 返回当前用户的未读通知数，供顶栏红点轮询。
func (s *NotificationService) Unread(ctx context.Context, userID int64) (*dto.NotificationUnread, error) {
	count, err := s.repo.CountUnreadNotifications(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &dto.NotificationUnread{UnreadCount: count}, nil
}

// MarkRead 标记已读：id>0 标记单条，id<=0 清空全部未读，返回最新的未读数。
func (s *NotificationService) MarkRead(ctx context.Context, userID, id int64) (*dto.NotificationUnread, error) {
	var err error
	if id > 0 {
		err = s.repo.MarkNotificationRead(ctx, userID, id)
	} else {
		err = s.repo.MarkAllNotificationsRead(ctx, userID)
	}
	if err != nil {
		return nil, err
	}
	return s.Unread(ctx, userID)
}

// CommentLiked 在「评论被点赞」时写入通知。
//
// 接收者是评论作者；自己赞自己的评论不提醒。这里只负责写入，
// 调用方按 best-effort 处理错误（通知失败不应影响点赞本身）。
func (s *NotificationService) CommentLiked(ctx context.Context, actorID, commentID int64) error {
	c, err := s.repo.GetCommentByID(ctx, commentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// 评论已被删除、或作者就是点赞人自己，都没有提醒的必要
	if c.Status == model.CommentStatusDeleted || c.UserID == actorID {
		return nil
	}

	return s.repo.CreateNotification(ctx, &model.Notification{
		ID:       snowflake.NextID(),
		UserID:   c.UserID,
		ActorID:  actorID,
		Type:     model.NotifyTypeCommentLiked,
		TargetID: commentID,
		VideoID:  c.VideoID,
		Content:  snapshotContent(c.Content),
		IsRead:   model.NotifyUnread,
	})
}

// VideoCommented 在「视频收到新评论」时写入通知。
//
// 接收者是视频上传者；自己评论自己的视频不提醒。上传者信息经视频服务获取。
func (s *NotificationService) VideoCommented(ctx context.Context, actorID, videoID, commentID int64, content string) error {
	v, err := s.videos.GetVideo(ctx, videoID)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	if v.UploaderID <= 0 || v.UploaderID == actorID {
		return nil
	}

	return s.repo.CreateNotification(ctx, &model.Notification{
		ID:       snowflake.NextID(),
		UserID:   v.UploaderID,
		ActorID:  actorID,
		Type:     model.NotifyTypeVideoCommented,
		TargetID: commentID,
		VideoID:  videoID,
		Content:  snapshotContent(content),
		IsRead:   model.NotifyUnread,
	})
}

// CommentReplied 在「评论收到回复」时写入通知。
//
// 接收者是被回复的那条评论的作者（回复某条回复时即该回复的作者）；
// 自己回复自己不提醒。TargetID 用新回复的评论 ID，每次回复都各自生成一条通知。
func (s *NotificationService) CommentReplied(ctx context.Context, actorID, recipientID, videoID, commentID int64, content string) error {
	if recipientID <= 0 || recipientID == actorID {
		return nil
	}
	return s.repo.CreateNotification(ctx, &model.Notification{
		ID:       snowflake.NextID(),
		UserID:   recipientID,
		ActorID:  actorID,
		Type:     model.NotifyTypeCommentReplied,
		TargetID: commentID,
		VideoID:  videoID,
		Content:  snapshotContent(content),
		IsRead:   model.NotifyUnread,
	})
}

// ContentRemoved 在「内容被管理员删除」时通知作者。
//
// 与其它三种通知的差别：接收者不是「被互动的人」，而是被处置内容的作者；
// ActorID 记管理员（便于日后审计「是谁删的」），但前端对这两类只展示固定文案，
// 不把管理员昵称暴露给普通用户。
//
// 自己删自己的内容（作者 == 操作者）不提醒。
func (s *NotificationService) ContentRemoved(ctx context.Context, kind int8, operatorID, authorID, videoID, targetID int64, content string) error {
	if authorID <= 0 || authorID == operatorID {
		return nil
	}
	return s.repo.CreateNotification(ctx, &model.Notification{
		ID:       snowflake.NextID(),
		UserID:   authorID,
		ActorID:  operatorID,
		Type:     kind,
		TargetID: targetID,
		VideoID:  videoID,
		Content:  snapshotContent(content),
		IsRead:   model.NotifyUnread,
	})
}

// snapshotContent 截取评论内容快照：去掉首尾空白，超长时按字符截断并加省略号。
func snapshotContent(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= notificationSnapshotRunes {
		return s
	}
	return string([]rune(s)[:notificationSnapshotRunes]) + "…"
}

// toNotificationInfo 把查询结果（触发者信息已由 fillNotificationUsers 补全）转换为对外 DTO。
func toNotificationInfo(row *repository.NotificationWithActor) dto.NotificationInfo {
	return dto.NotificationInfo{
		ID:         row.ID,
		Type:       row.Type,
		ActorID:    row.ActorID,
		Nickname:   row.Nickname,
		Avatar:     row.Avatar,
		VideoID:    row.VideoID,
		VideoTitle: row.VideoTitle,
		Content:    row.Content,
		IsRead:     row.IsRead == model.NotifyRead,
		CreatedAt:  row.CreatedAt,
	}
}
