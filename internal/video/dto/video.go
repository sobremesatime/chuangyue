// Package dto 定义视频域的请求与响应结构，字段与 OpenAPI 契约保持一致。
package dto

import (
	"io"
	"time"
)

// SortHot 是视频列表查询中「按热度排序」的取值。
const SortHot = "hot"

// AdminListQuery 是管理后台视频列表的查询条件。
//
// 与面向普通用户的视频列表查询不同：不按分类过滤，但可按状态过滤。
// Status 用指针表示「未传」：0 是合法状态值（转码中），
// 若用数字 0 兜底会与「不筛选」混淆。
type AdminListQuery struct {
	Page     int
	PageSize int
	Status   *int8
	// Keyword 对标题与简介做子串匹配；空串表示不过滤。
	Keyword string
}

// UpdateStatusInput 是管理后台上下架视频的输入。
//
// Status 用指针以区分「字段缺失」与「传了 0」：缺失时返回参数错误。
type UpdateStatusInput struct {
	Status *int8 `json:"status"`
}

// UpdateVideoInput 是管理后台编辑视频的输入。
//
// 字段用指针区分「未传」与「传了空值」：简介允许被清空成空串，
// 而未传时表示不修改。视频片源本身不可替换，只允许改元信息与封面。
type UpdateVideoInput struct {
	Title       *string
	Description *string
	CategoryID  *int64

	// 封面为可选项：Cover 非 nil 表示要替换封面。
	CoverFilename string
	CoverSize     int64
	Cover         io.Reader
}

// UploadInput 是上传视频所需的输入。
//
// 用 io.Reader 而不是 *multipart.FileHeader，让 service 层不依赖 gin，
// 便于后续被 gRPC 接口复用。
type UploadInput struct {
	Title       string
	Description string
	CategoryID  int64

	Filename string
	Size     int64
	File     io.Reader

	// 封面为可选项，为空时 Cover 为 nil。
	CoverFilename string
	CoverSize     int64
	Cover         io.Reader
}

// VideoInfo 是对外暴露的视频信息。
type VideoInfo struct {
	// ID 以字符串序列化：雪花 ID 超出 JavaScript 安全整数上限，按数字传输会导致前端精度丢失。
	ID          int64  `json:"id,string"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
	PlayURL     string `json:"play_url"`
	Duration    int    `json:"duration"`
	CategoryID  int64  `json:"category_id"`
	ViewCount   int64  `json:"view_count"`
	LikeCount   int64  `json:"like_count"`
	// FavoriteCount 为视频被收藏的总数
	FavoriteCount int64 `json:"favorite_count"`
	Status        int8  `json:"status"`
	// ErrorMsg 为转码失败原因，仅管理端需要展示，正常状态下为空串。
	ErrorMsg  string    `json:"error_msg"`
	CreatedAt time.Time `json:"created_at"`
}

// VideoBrief 是对外暴露的「精简卡片」视频信息。
//
// 与 VideoInfo 的关系：VideoInfo 是完整卡片（详情页 / 视频列表用），VideoBrief 只含
// 推荐流、搜索结果这类场景渲染卡片所需的字段，与跨服务契约 video.v1.VideoBrief 一一对应。
// 之所以定义在视频域而不是各消费域：这个形状由视频域定义，feed / search 只是使用者——
// 与 videodto.VideoInfo 被互动域「我的」列表复用是同一个先例。
type VideoBrief struct {
	// ID 以字符串序列化：雪花 ID 超出 JavaScript 安全整数上限，按数字传输会导致前端精度丢失。
	ID         int64     `json:"id,string"`
	Title      string    `json:"title"`
	UploaderID int64     `json:"uploader_id,string"`
	CoverURL   string    `json:"cover_url"`
	CategoryID int64     `json:"category_id"`
	Status     int8      `json:"status"`
	Duration   int       `json:"duration"`
	ViewCount  int64     `json:"view_count"`
	CreatedAt  time.Time `json:"created_at"`
}
