// Package dto 定义互动域的请求与响应结构，字段与 OpenAPI 契约保持一致。
package dto

import "time"

// VideoInfo 是互动域自视频服务取回的视频信息载体，仅在互动域内部使用。
//
// 之所以不复用对外 DTO（internal/video/dto.VideoInfo）：这里额外带 uploader_id ——
// 「视频收到新评论」通知需要通知上传者，但这属于互动域的内部需求，不该为了让互动域
// 拿到它而改动对外的视频 DTO 契约。
type VideoInfo struct {
	ID          int64
	Title       string
	Description string
	CoverURL    string
	PlayURL     string
	Duration    int
	CategoryID  int64
	UploaderID  int64
	ViewCount   int64
	Status      int8
	ErrorMsg    string
	CreatedAt   time.Time
}
