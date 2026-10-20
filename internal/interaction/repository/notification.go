// Package repository 负责通知表的数据访问。
package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
)

// NotificationWithActor 是通知查询的落点：通知本体 + 触发者展示信息 + 视频标题。
//
// Nickname / Avatar / VideoTitle 由 service 层分别经 UserProvider / VideoProvider 补全，
// 仓储不再 JOIN `user` 或视频表。
type NotificationWithActor struct {
	model.Notification
	Nickname   string `gorm:"column:nickname"`
	Avatar     string `gorm:"column:avatar"`
	VideoTitle string `gorm:"column:video_title"`
}

// notificationSelect 是通知列表显式选择的列；视频标题交由 service 层经视频服务补全，
// 触发者昵称 / 头像由 UserProvider 补全，仓储只返回通知自身字段。
const notificationSelect = "n.id, n.user_id, n.actor_id, n.type, n.target_id, n.video_id, " +
	"n.content, n.is_read, n.created_at"

// CreateNotification 插入一条通知。
//
// 幂等：唯一键 (user_id, actor_id, type, target_id) 兜住重复触发，
// 冲突时本次插入无行变更，也不会报错——同一个人对同一条评论的点赞只提醒一次。
func (r *Repository) CreateNotification(ctx context.Context, n *model.Notification) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(n).
		Error
}

// ListNotifications 分页查询某个用户收到的通知，按时间倒序。
//
// 拆库后不再 LEFT JOIN 视频表：视频标题由 service 层经视频服务补全（缺失即留空）；
// 触发者昵称 / 头像同样由 service 层经 UserProvider 补全，列表不出现空洞。
func (r *Repository) ListNotifications(ctx context.Context, userID int64, offset, limit int) ([]NotificationWithActor, int64, error) {
	base := r.db.WithContext(ctx).
		Table("notification AS n").
		Where("n.user_id = ?", userID)

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []NotificationWithActor
	err := base.
		Select(notificationSelect).
		Order("n.created_at DESC, n.id DESC").
		Offset(offset).Limit(limit).
		Scan(&list).
		Error
	return list, total, err
}

// CountUnreadNotifications 统计该用户的未读通知数，供顶栏红点轮询。
func (r *Repository) CountUnreadNotifications(ctx context.Context, userID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, model.NotifyUnread).
		Count(&count).
		Error
	return count, err
}

// MarkNotificationRead 把某条通知标记为已读（只能操作自己的通知，幂等）。
func (r *Repository) MarkNotificationRead(ctx context.Context, userID, id int64) error {
	return r.db.WithContext(ctx).
		Model(&model.Notification{}).
		Where("id = ? AND user_id = ? AND is_read = ?", id, userID, model.NotifyUnread).
		Update("is_read", model.NotifyRead).
		Error
}

// MarkAllNotificationsRead 把该用户的全部未读通知标记为已读。
func (r *Repository) MarkAllNotificationsRead(ctx context.Context, userID int64) error {
	return r.db.WithContext(ctx).
		Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, model.NotifyUnread).
		Update("is_read", model.NotifyRead).
		Error
}
