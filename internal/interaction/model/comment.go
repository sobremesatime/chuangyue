// Package model 定义互动域的数据模型。
//
// 本文件承载视频评论：一级评论直接挂在视频下，回复统一扁平挂在一级评论下（parent_id 指向一级评论），
// 不再嵌套第三层。
package model

import "time"

// 评论状态。
const (
	CommentStatusNormal  int8 = 1 // 正常
	CommentStatusDeleted int8 = 2 // 已删除（软删，保留行以维持审计与计数稳定）
)

// VideoComment 对应视频评论表。
type VideoComment struct {
	ID       int64 `gorm:"column:id;primaryKey"`
	VideoID  int64 `gorm:"column:video_id"`
	UserID   int64 `gorm:"column:user_id"`
	ParentID int64 `gorm:"column:parent_id"`
	// ReplyToUserID 是被回复者用户 ID：回复某条回复时用于「回复 @某人」展示，0 表示未指定
	ReplyToUserID int64     `gorm:"column:reply_to_user_id"`
	Content       string    `gorm:"column:content"`
	LikeCount     int64     `gorm:"column:like_count"`
	ReplyCount    int64     `gorm:"column:reply_count"`
	Status        int8      `gorm:"column:status"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (VideoComment) TableName() string {
	return "video_comment"
}

// VideoCommentLike 对应评论点赞关系表，以 (user_id, comment_id) 为唯一键保证幂等。
type VideoCommentLike struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	CommentID int64     `gorm:"column:comment_id"`
	UserID    int64     `gorm:"column:user_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (VideoCommentLike) TableName() string {
	return "video_comment_like"
}
