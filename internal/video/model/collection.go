package model

import "time"

// 合集状态。
//
// 与视频状态分开定义而不是复用 StatusXxx：两者语义无关，
// 合集只有「草稿 / 公开」两态，硬套视频的四态会让读侧判断变得含糊。
const (
	CollectionStatusDraft  int8 = 0 // 草稿：仅管理端可见
	CollectionStatusPublic int8 = 1 // 公开：普通用户可见
)

// Collection 对应视频合集表。
type Collection struct {
	ID          int64  `gorm:"column:id;primaryKey"`
	Title       string `gorm:"column:title;size:128"`
	Description string `gorm:"column:description;size:512"`
	// CoverURL 为空时，读取侧用合集内第一个视频的封面兜底（见 service 层），
	// 不在库里固化：视频换封面后合集封面能跟着变。
	CoverURL   string `gorm:"column:cover_url;size:255"`
	UploaderID int64  `gorm:"column:uploader_id"`
	Status     int8   `gorm:"column:status"`
	// VideoCount 为冗余计数，与关联行在同一事务内重算，避免合集列表按条 COUNT。
	VideoCount int64     `gorm:"column:video_count"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (Collection) TableName() string {
	return "collection"
}

// CollectionVideo 对应合集与视频的关联表。
//
// Sort 是合集内的播放序号，由管理端显式维护；允许重复（并发追加时可能撞号），
// 读侧按 (sort, id) 排序兜底，保证顺序稳定可复现。
type CollectionVideo struct {
	ID           int64     `gorm:"column:id;primaryKey"`
	CollectionID int64     `gorm:"column:collection_id"`
	VideoID      int64     `gorm:"column:video_id"`
	Sort         int       `gorm:"column:sort"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

// TableName 显式指定表名。
func (CollectionVideo) TableName() string {
	return "collection_video"
}
