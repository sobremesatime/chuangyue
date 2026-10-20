// Package model 定义互动域的计数模型。
package model

import "time"

// VideoStat 对应互动域的 video_stat 表：视频互动计数的真源。
//
// 拆库前 like_count / favorite_count 是 video 表的列、由互动域跨表维护；
// 「计数归位」后这两个计数的真源搬到这里（互动域自持），video 表只保留一份
// 供热度排序的只读副本，由 video.stat 主题异步回写。
//
// 不设自增主键，直接用 video_id 作主键：一个视频一行，天然幂等。
type VideoStat struct {
	VideoID       int64     `gorm:"column:video_id;primaryKey"`
	LikeCount     int64     `gorm:"column:like_count"`
	FavoriteCount int64     `gorm:"column:favorite_count"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (VideoStat) TableName() string {
	return "video_stat"
}
