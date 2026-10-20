// Package dto 定义互动域弹幕能力的请求与响应结构，字段与 OpenAPI 契约保持一致。
package dto

import "time"

// DanmakuInfo 是对外暴露的弹幕信息。
//
// 带上发送者的昵称与头像：后续版本做弹幕溯源展示时可直接使用，避免二次查询。
// 雪花 ID 一律以字符串序列化，避免 JavaScript 精度丢失。
type DanmakuInfo struct {
	ID        int64     `json:"id,string"`
	VideoID   int64     `json:"video_id,string"`
	UserID    int64     `json:"user_id,string"`
	Username  string    `json:"username"`
	Nickname  string    `json:"nickname"`
	Avatar    string    `json:"avatar"`
	Content   string    `json:"content"`
	TimeMs    int       `json:"time_ms"`
	Color     string    `json:"color"`
	Mode      int8      `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateDanmakuInput 是发表弹幕的请求体。
type CreateDanmakuInput struct {
	Content string `json:"content"`
	TimeMs  int    `json:"time_ms"`
	Color   string `json:"color"`
	Mode    int8   `json:"mode"`
}
