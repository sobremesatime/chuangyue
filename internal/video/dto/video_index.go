package dto

import "time"

// 视频索引事件的动作。
const (
	// VideoIndexUpsert 表示写入 / 覆盖索引文档（视频处于已上架状态）。
	VideoIndexUpsert = "upsert"
	// VideoIndexDelete 表示从索引中移除（下架 / 转码失败 / 删除）。
	VideoIndexDelete = "delete"
)

// VideoIndexEvent 是「视频变更同步到检索索引」的消息体。
//
// 只同步检索需要的字段：标题、简介、分类、创建时间。封面 / 播放量 / 时长等展示字段
// 不放进索引——检索只产出有序的 video_id，卡片由搜索服务向视频服务批量补全，
// 这样播放量这类高频变化的字段不会让索引成为一份需要持续追平的副本。
type VideoIndexEvent struct {
	Action  string         `json:"action"`
	Video   *VideoIndexDoc `json:"video,omitempty"`
	VideoID int64          `json:"video_id,omitempty"`
}

// VideoIndexDoc 是索引文档。
//
// 字段名与 pkg/es 里的索引映射一一对应；created_at 用 RFC3339 时间，
// 由 ES 的 date 类型解析，同时作为默认排序字段。
type VideoIndexDoc struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CategoryID  int64     `json:"category_id"`
	CreatedAt   time.Time `json:"created_at"`
}
