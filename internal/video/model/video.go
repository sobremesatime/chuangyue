// Package model 定义视频域的数据模型（与 video 表一一对应）。
package model

import "time"

// 视频状态。
const (
	StatusTranscoding int8 = 0 // 转码中：已上传落盘，等待 Worker 产出 HLS
	StatusPublished   int8 = 1 // 已上架，可被普通用户看到
	StatusOffline     int8 = 2 // 已下架
	StatusFailed      int8 = 3 // 转码失败：流水线最终失败，需管理员介入
)

// Video 对应视频表。
type Video struct {
	ID          int64  `gorm:"column:id;primaryKey"`
	Title       string `gorm:"column:title;size:128"`
	Description string `gorm:"column:description;size:512"`
	CoverURL    string `gorm:"column:cover_url;size:255"`
	PlayURL     string `gorm:"column:play_url;size:255"`
	// SourceURL 是上传的原始文件地址，转码的输入；保留它便于后续按新参数重转。
	SourceURL  string `gorm:"column:source_url;size:255"`
	Duration   int    `gorm:"column:duration"`
	CategoryID int64  `gorm:"column:category_id"`
	UploaderID int64  `gorm:"column:uploader_id"`
	Status     int8   `gorm:"column:status"`
	// ErrorMsg 记录转码失败原因，仅在 Status 为 StatusFailed 时有值。
	ErrorMsg  string `gorm:"column:error_msg;size:512"`
	ViewCount int64  `gorm:"column:view_count"`
	LikeCount int64  `gorm:"column:like_count"`
	// FavoriteCount 为冗余计数，与 LikeCount 一样在收藏 / 取消收藏的事务里同步维护
	FavoriteCount int64     `gorm:"column:favorite_count"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (Video) TableName() string {
	return "video"
}
