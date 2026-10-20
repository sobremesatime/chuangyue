// Package dto 定义互动域评论能力的请求与响应结构，字段与 OpenAPI 契约保持一致。
package dto

import "time"

// CommentInfo 是对外暴露的评论信息。
//
// 带上作者的昵称与头像：详情页直接展示，避免前端为每条评论再查一次用户资料。
// 雪花 ID 一律以字符串序列化，避免 JavaScript 精度丢失。
type CommentInfo struct {
	ID       int64  `json:"id,string"`
	VideoID  int64  `json:"video_id,string"`
	UserID   int64  `json:"user_id,string"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
	Content  string `json:"content"`
	// LikeCount 为该评论的点赞总数；Liked 表示当前登录用户是否已点赞（未登录恒为 false）
	LikeCount int64     `json:"like_count"`
	Liked     bool      `json:"liked"`
	CreatedAt time.Time `json:"created_at"`

	// 回复相关字段：ParentID 为所属一级评论 ID（"0" 表示本身是一级评论）
	ParentID int64 `json:"parent_id,string"`
	// ReplyToUserID / ReplyToNickname 用于「回复 @某人」展示，未指定时 ReplyToUserID 为 "0"、昵称为空串
	ReplyToUserID   int64  `json:"reply_to_user_id,string"`
	ReplyToNickname string `json:"reply_to_nickname"`
	// ReplyCount 是一级评论的回复总数
	ReplyCount int64 `json:"reply_count"`
	// Replies 内嵌一级评论最早的若干条回复（一级评论才有；回复项自身为空数组）
	Replies []CommentInfo `json:"replies"`
}

// CommentLikeResult 是评论点赞 / 取消点赞的结果。
type CommentLikeResult struct {
	Liked     bool  `json:"liked"`
	LikeCount int64 `json:"like_count"`
}

// CreateCommentInput 是发表评论 / 回复的请求体。
type CreateCommentInput struct {
	Content string `json:"content"`
	// ParentID 为被回复的评论 ID：指向一级评论表示直接回复该评论，指向某条回复表示回复该回复
	// （后端统一扁平化到同一一级评论下）。为 "0" 或省略表示发表一级评论。
	ParentID int64 `json:"parent_id,string"`
}

// CommentListQuery 是评论列表的查询条件。
type CommentListQuery struct {
	Page     int
	PageSize int
}
