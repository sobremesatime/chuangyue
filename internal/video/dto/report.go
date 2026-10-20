// Package dto 定义视频域内容治理能力的请求与响应结构，字段与 OpenAPI 契约保持一致。
package dto

import "time"

// ReportInfo 是对外暴露的举报信息。
//
// 带上举报人昵称与相关视频标题：管理端列表直接展示，避免逐条再查一次用户资料与视频。
// 雪花 ID 一律以字符串序列化，避免 JavaScript 精度丢失。
type ReportInfo struct {
	ID               int64      `json:"id,string"`
	TargetType       int8       `json:"target_type"`
	TargetID         int64      `json:"target_id,string"`
	VideoID          int64      `json:"video_id,string"`
	VideoTitle       string     `json:"video_title"`
	ReporterID       int64      `json:"reporter_id,string"`
	ReporterNickname string     `json:"reporter_nickname"`
	Reason           string     `json:"reason"`
	Detail           string     `json:"detail"`
	Status           int8       `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	HandledAt        *time.Time `json:"handled_at"`
}

// CreateReportInput 是发起举报的请求体。
type CreateReportInput struct {
	TargetType int8   `json:"target_type"`
	TargetID   int64  `json:"target_id,string"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail"`
}

// HandleReportInput 是管理端处理举报的请求体。
type HandleReportInput struct {
	Status int8 `json:"status"` // 只允许 1 已处理 或 2 已忽略
}

// AdminReportListQuery 是管理端举报列表的查询条件。
type AdminReportListQuery struct {
	Page     int
	PageSize int
	Status   *int8 // nil 表示不按状态过滤
}

// AdminCommentInfo 是管理端内容治理下的评论条目。
//
// 带上所属视频标题与作者、被回复者信息，列表可直接定位与展示，无需二次查询。
type AdminCommentInfo struct {
	ID              int64     `json:"id,string"`
	VideoID         int64     `json:"video_id,string"`
	VideoTitle      string    `json:"video_title"`
	UserID          int64     `json:"user_id,string"`
	Nickname        string    `json:"nickname"`
	Avatar          string    `json:"avatar"`
	Content         string    `json:"content"`
	ParentID        int64     `json:"parent_id,string"`
	ReplyToNickname string    `json:"reply_to_nickname"`
	ReplyCount      int64     `json:"reply_count"`
	LikeCount       int64     `json:"like_count"`
	CreatedAt       time.Time `json:"created_at"`
}

// AdminDanmakuInfo 是管理端内容治理下的弹幕条目。
type AdminDanmakuInfo struct {
	ID         int64     `json:"id,string"`
	VideoID    int64     `json:"video_id,string"`
	VideoTitle string    `json:"video_title"`
	UserID     int64     `json:"user_id,string"`
	Nickname   string    `json:"nickname"`
	Content    string    `json:"content"`
	TimeMs     int       `json:"time_ms"`
	Mode       int8      `json:"mode"`
	CreatedAt  time.Time `json:"created_at"`
}

// AdminContentQuery 是管理端内容（评论 / 弹幕）治理列表的查询条件。
type AdminContentQuery struct {
	Page     int
	PageSize int
	Keyword  string
}
