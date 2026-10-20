// Package dto 定义转码域的消息载荷。
//
// 报文只走内部消息队列，不对外暴露，字段与文档
// 《窗月-异步转码流水线实现逻辑》的消息契约保持一致。
package dto

// Task 是一条转码任务。
type Task struct {
	// VideoID 为视频主键，Worker 据此定位数据库记录与输出目录。
	VideoID int64 `json:"video_id"`
	// SourceURL 为源文件的对外地址，Worker 还原为本地路径后作为 FFmpeg 输入。
	SourceURL string `json:"source_url"`
	// Attempt 为第几次尝试，用于日志追踪。
	Attempt int `json:"attempt"`
}
