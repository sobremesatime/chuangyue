// Package model 定义视频域的数据模型。
//
// 本文件承载内容举报：用户对视频或评论发起举报，管理端按状态跟进处理。
package model

import "time"

// 举报对象类型。
const (
	ReportTargetVideo   int8 = 1 // 视频
	ReportTargetComment int8 = 2 // 评论
)

// 举报处理状态。
const (
	ReportStatusPending int8 = 0 // 待处理
	ReportStatusHandled int8 = 1 // 已处理
	ReportStatusIgnored int8 = 2 // 已忽略
)

// VideoReport 对应内容举报表。
type VideoReport struct {
	ID         int64 `gorm:"column:id;primaryKey"`
	UserID     int64 `gorm:"column:user_id"`
	TargetType int8  `gorm:"column:target_type"`
	TargetID   int64 `gorm:"column:target_id"`
	// VideoID 是举报对象所在视频 ID：举报视频时即视频本身，举报评论时为评论所属视频，供管理端跳转定位
	VideoID   int64  `gorm:"column:video_id"`
	Reason    string `gorm:"column:reason"`
	Detail    string `gorm:"column:detail"`
	Status    int8   `gorm:"column:status"`
	HandledBy int64  `gorm:"column:handled_by"`
	// HandledAt 是处理时间，未处理时为空
	HandledAt *time.Time `gorm:"column:handled_at"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (VideoReport) TableName() string {
	return "video_report"
}
