// Package model 定义互动域的数据模型。
//
// 本文件承载视频弹幕：与视频时间轴绑定的即时评论，播放时按时间点叠加在画面上。
package model

import "time"

// 弹幕展示模式。
const (
	DanmakuModeScroll int8 = 1 // 滚动（从右向左）
	DanmakuModeTop    int8 = 2 // 顶部固定
	DanmakuModeBottom int8 = 3 // 底部固定
)

// 弹幕状态。
const (
	DanmakuStatusNormal  int8 = 1 // 正常
	DanmakuStatusDeleted int8 = 2 // 已删除（软删，保留行以维持审计与计数稳定）
)

// VideoDanmaku 对应视频弹幕表。
type VideoDanmaku struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	VideoID   int64     `gorm:"column:video_id"`
	UserID    int64     `gorm:"column:user_id"`
	Content   string    `gorm:"column:content"`
	TimeMs    int       `gorm:"column:time_ms"`
	Color     string    `gorm:"column:color"`
	Mode      int8      `gorm:"column:mode"`
	Status    int8      `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (VideoDanmaku) TableName() string {
	return "video_danmaku"
}
