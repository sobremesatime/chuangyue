package dto

// 上传会话的模式。
const (
	// UploadModeInstant 表示秒传：服务端已有相同文件，直接登记视频。
	UploadModeInstant = "instant"
	// UploadModeMultipart 表示需要分片上传。
	UploadModeMultipart = "multipart"
)

// InitUploadRequest 是初始化分片上传的输入。
//
// 文件指纹（FileMD5）是秒传与断点续传的唯一依据，由前端在上传前算好。
type InitUploadRequest struct {
	FileMD5     string `json:"file_md5" binding:"required,len=32"`
	Filename    string `json:"filename" binding:"required,max=255"`
	TotalSize   int64  `json:"total_size" binding:"required,gt=0"`
	ContentType string `json:"content_type" binding:"max=128"`
	Title       string `json:"title" binding:"max=128"`
	Description string `json:"description" binding:"max=512"`
	CategoryID  int64  `json:"category_id"`
}

// UploadedPart 是一个已上传分片的对外描述。
type UploadedPart struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

// InitUploadResponse 是初始化 / 查询会话的结果。
//
// Mode 为 instant 时只填 VideoID；为 multipart 时填会话与分片计划，
// Uploaded 列出已上传分片，前端据此跳过，实现断点续传。
type InitUploadResponse struct {
	Mode      string         `json:"mode"`
	UploadID  string         `json:"upload_id,omitempty"`
	ChunkSize int64          `json:"chunk_size,omitempty"`
	PartTotal int            `json:"part_total,omitempty"`
	Uploaded  []UploadedPart `json:"uploaded,omitempty"`
	VideoID   string         `json:"video_id,omitempty"`
}

// PresignPartResponse 是分片预签名直传地址。
type PresignPartResponse struct {
	PartNumber int32  `json:"part_number"`
	URL        string `json:"url"`
}

// CompleteUploadRequest 是完成分片上传的输入。
//
// Parts 可选：直传模式下前端回传各片 ETag，省略时服务端以已记录的分片为准。
type CompleteUploadRequest struct {
	Parts []UploadedPart `json:"parts"`
}
