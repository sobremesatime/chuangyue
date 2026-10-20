// Package model 定义互动域的数据模型。
//
// 本文件承载用户与视频之间的互动关系：点赞、收藏、观看历史。
// 三张表都以 (user_id, video_id) 为唯一键，保证同一用户对同一视频只有一条记录。
package model

import "time"

// VideoLike 对应视频点赞表。
type VideoLike struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	UserID    int64     `gorm:"column:user_id"`
	VideoID   int64     `gorm:"column:video_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName 显式指定表名。
func (VideoLike) TableName() string {
	return "video_like"
}

// VideoFavorite 对应视频收藏表。
type VideoFavorite struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	UserID    int64     `gorm:"column:user_id"`
	VideoID   int64     `gorm:"column:video_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName 显式指定表名。
func (VideoFavorite) TableName() string {
	return "video_favorite"
}

// VideoWatchLater 对应稍后再看表。
//
// 与收藏是两条独立的列表：收藏表达「喜欢」，稍后再看表达「待看」，
// 因此单独一张表，而不是复用 video_favorite。
type VideoWatchLater struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	UserID    int64     `gorm:"column:user_id"`
	VideoID   int64     `gorm:"column:video_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName 显式指定表名。
func (VideoWatchLater) TableName() string {
	return "video_watch_later"
}

// VideoWatchHistory 对应观看历史表。
//
// Progress 记录播放进度（秒），同一用户对同一视频只保留一条，
// 再次观看时以最新进度覆盖，因此列表按 updated_at 倒序即可反映最近观看顺序。
type VideoWatchHistory struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	UserID    int64     `gorm:"column:user_id"`
	VideoID   int64     `gorm:"column:video_id"`
	Progress  int       `gorm:"column:progress"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (VideoWatchHistory) TableName() string {
	return "video_watch_history"
}
