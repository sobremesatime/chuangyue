// Package model 定义互动域的数据模型。
//
// 本文件承载站内通知：评论被点赞、视频收到评论、评论收到回复三类提醒。
package model

import "time"

// 通知类型。
const (
	NotifyTypeCommentLiked   int8 = 1 // 你的评论被点赞
	NotifyTypeVideoCommented int8 = 2 // 你上传的视频收到新评论
	NotifyTypeCommentReplied int8 = 3 // 你的评论收到回复
	// 管理动作：内容被管理员删除时告知作者。站内通知是目前唯一能让当事人知道
	// 「内容为什么不见了」的渠道——否则用户只会看到评论凭空消失。
	NotifyTypeCommentRemoved int8 = 4 // 你的评论被管理员删除
	NotifyTypeDanmakuRemoved int8 = 5 // 你的弹幕被管理员删除
)

// 通知的已读状态。
const (
	NotifyUnread int8 = 0 // 未读
	NotifyRead   int8 = 1 // 已读
)

// Notification 对应通知表。
//
// 通知在写入时就把展示需要的内容快照下来（评论正文、视频 ID），
// 这样列表查询不必再去回表评论与视频；触发者的昵称与头像在查询时联 user 表取。
type Notification struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	UserID    int64     `gorm:"column:user_id"`   // 接收者
	ActorID   int64     `gorm:"column:actor_id"`  // 触发者
	Type      int8      `gorm:"column:type"`      // 见 NotifyType 常量
	TargetID  int64     `gorm:"column:target_id"` // 目标（评论）ID
	VideoID   int64     `gorm:"column:video_id"`
	Content   string    `gorm:"column:content;size:255"`
	IsRead    int8      `gorm:"column:is_read"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName 显式指定表名。
func (Notification) TableName() string {
	return "notification"
}
