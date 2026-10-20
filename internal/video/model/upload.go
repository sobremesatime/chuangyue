// Package model 定义上传会话与分片的数据模型（与 upload_session / upload_part 表一一对应）。
package model

import "time"

// 上传会话状态。
const (
	UploadStatusUploading int8 = 1 // 上传中：会话已创建，分片尚未传完
	UploadStatusCompleted int8 = 2 // 已完成：分片合并、视频记录与转码任务均已就绪
	UploadStatusAborted   int8 = 3 // 已中止：主动取消或清理，分片已释放
)

// UploadSession 对应分片上传会话表。
//
// 它承载业务语义（谁传的、整文件 MD5、能否秒传、归属视频），
// 与驱动侧的 uploadID 一一对应但职责不同。
type UploadSession struct {
	ID          int64  `gorm:"column:id;primaryKey"`
	UserID      int64  `gorm:"column:user_id"`
	FileMD5     string `gorm:"column:file_md5;size:32"`
	Filename    string `gorm:"column:filename;size:255"`
	ContentType string `gorm:"column:content_type;size:128"`
	Title       string `gorm:"column:title;size:128"`
	Description string `gorm:"column:description;size:512"`
	CategoryID  int64  `gorm:"column:category_id"`
	TotalSize   int64  `gorm:"column:total_size"`
	ChunkSize   int64  `gorm:"column:chunk_size"`
	PartTotal   int    `gorm:"column:part_total"`
	StorageKey  string `gorm:"column:storage_key;size:512"`
	Driver      string `gorm:"column:driver;size:16"`
	UploadID    string `gorm:"column:upload_id;size:512"`
	Status      int8   `gorm:"column:status"`
	// VideoID 在 complete 成功后写入，未完成时为空。
	VideoID   *int64    `gorm:"column:video_id"`
	ExpiresAt time.Time `gorm:"column:expires_at"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名。
func (UploadSession) TableName() string {
	return "upload_session"
}

// UploadPart 对应分片上传明细表。
//
// 主键为 (session_id, part_number)，因此同一分片重复上传天然幂等。
type UploadPart struct {
	SessionID  int64     `gorm:"column:session_id;primaryKey"`
	PartNumber int32     `gorm:"column:part_number;primaryKey"`
	ETag       string    `gorm:"column:etag;size:128"`
	Size       int64     `gorm:"column:size"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// TableName 显式指定表名。
func (UploadPart) TableName() string {
	return "upload_part"
}
