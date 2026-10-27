package dto

import "time"

// CreateCollectionInput 是管理端新建合集的输入。
type CreateCollectionInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

// UpdateCollectionInput 是管理端编辑合集的输入。
//
// 字段用指针区分「未传」与「传了空值」：简介与封面允许被清空成空串
// （封面清空后读侧重新退回「用第一个视频封面兜底」），而未传表示不修改。
type UpdateCollectionInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	CoverURL    *string `json:"cover_url"`
	Status      *int8   `json:"status"`
}

// CollectionVideoIDsInput 是「批量加视频 / 重排顺序」共用的请求体。
//
// video_ids 用字符串数组：雪花 ID 超出 JavaScript 安全整数上限，
// 与响应里的 id 保持同一种字符串表示，避免前端解析后精度丢失。
type CollectionVideoIDsInput struct {
	VideoIDs []string `json:"video_ids"`
}

// CollectionInfo 是对外暴露的合集信息。
type CollectionInfo struct {
	// ID 以字符串序列化：雪花 ID 超出 JavaScript 安全整数上限，按数字传输会导致前端精度丢失。
	ID          int64  `json:"id,string"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
	UploaderID  int64  `json:"uploader_id,string"`
	Status      int8   `json:"status"`
	// VideoCount 是合集内的视频总数（含未上架），由写入路径在同一事务里维护。
	VideoCount int64     `json:"video_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// CollectionDetail 是合集详情：合集信息（平铺）+ 有序播放列表。
//
// 合集字段内嵌而不是包一层 collection：详情页与列表页共用同一套字段，
// 前端拿到详情后可以直接替换列表里的那一项，不必处理两种嵌套结构。
type CollectionDetail struct {
	CollectionInfo
	Videos []VideoInfo `json:"videos"`
}
