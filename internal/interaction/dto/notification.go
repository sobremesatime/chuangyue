// Package dto 定义互动域通知能力的请求与响应结构，字段与 OpenAPI 契约保持一致。
package dto

import "time"

// NotificationInfo 是对外暴露的一条通知。
//
// 带上触发者的昵称与头像、以及视频标题：消息中心直接展示，前端不必再逐个补资料。
// 雪花 ID 一律以字符串序列化，避免 JavaScript 精度丢失。
type NotificationInfo struct {
	ID int64 `json:"id,string"`
	// Type 取值见 model 的通知类型常量：1 评论被点赞、2 视频收到评论、3 评论收到回复
	Type     int8   `json:"type"`
	ActorID  int64  `json:"actor_id,string"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
	// VideoID 用于点击跳转到视频详情页；Content 是触发时的评论正文快照
	VideoID    int64     `json:"video_id,string"`
	VideoTitle string    `json:"video_title"`
	Content    string    `json:"content"`
	IsRead     bool      `json:"is_read"`
	CreatedAt  time.Time `json:"created_at"`
}

// NotificationUnread 是未读数查询与标记已读后的结果。
type NotificationUnread struct {
	UnreadCount int64 `json:"unread_count"`
}

// MarkNotificationsReadInput 是标记已读的请求体。
//
// ID 缺省（或传 0）表示「全部标记已读」，用于消息面板的一键已读。
type MarkNotificationsReadInput struct {
	ID int64 `json:"id,string"`
}
