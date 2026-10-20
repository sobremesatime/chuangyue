// Package storage 抽象对象存储能力，屏蔽「本地磁盘」与「对象存储」的差异。
//
// 设计遵循「先定接口、后补实现」：业务层只依赖 Storage 接口，
// 当前版本提供 local（本地磁盘）与 minio（对象存储）两种驱动，
// 新增驱动只需在 New 里加一个分支，业务代码无需改动。
//
// 单次写入（Put）与分片上传（Multipart）是两类能力：
// 前者够用于封面、小文件；后者用于大体积视频，支持断点续传与并行上传。
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sobremesatime/chuangyue/pkg/config"
)

// 支持的驱动名。
const (
	DriverLocal = "local" // 本地磁盘
	DriverMinIO = "minio" // 对象存储
)

// ErrNotSupported 表示当前驱动不支持所请求的能力。
//
// 例如 local 驱动没有预签名直传，调用方据此回退到「网关代传」。
var ErrNotSupported = errors.New("storage: 当前驱动不支持该能力")

// Part 是一个已上传分片的元信息，也是 CompleteMultipart 的入参。
type Part struct {
	Number int32  // 分片序号，从 1 开始
	ETag   string // 驱动返回的校验值，统一为不带引号的十六进制串
	Size   int64  // 分片字节数
}

// Storage 抽象对象存储。单次写入与分片上传都从这里走。
type Storage interface {
	// Driver 返回驱动名，便于日志与排障。
	Driver() string
	// Put 一次性写入对象，供小文件与封面使用。
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Delete 删除对象，不存在时视为成功。
	Delete(ctx context.Context, key string) error
	// Open 打开对象用于读取，供需要读取对象内容的调用方（如转码 Worker）使用。
	// 调用方负责关闭返回的 ReadCloser。
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// URL 返回可对外访问的地址。
	URL(key string) string
	// KeyFromURL 把对外访问地址反解为对象 key；地址不属于本驱动时返回 false。
	KeyFromURL(url string) (key string, ok bool)
	// PresignPut 返回单次直传的预签名地址；不支持时返回 ErrNotSupported。
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
	// Multipart 返回分片上传能力；不支持时返回 ErrNotSupported。
	//
	// 用「子接口 + 取用方」而不是把分片方法全塞进 Storage，
	// 让只做简单读写的调用方不必感知分片概念。
	Multipart() (MultipartUploader, error)
}

// MultipartUploader 是分片上传能力（对应 S3 Multipart API 语义）。
type MultipartUploader interface {
	// InitMultipart 创建分片会话，返回驱动侧 uploadID。
	InitMultipart(ctx context.Context, key, contentType string) (uploadID string, err error)
	// PresignPart 返回某一片的预签名直传地址；不支持时返回 ErrNotSupported。
	PresignPart(ctx context.Context, key, uploadID string, number int32, ttl time.Duration) (string, error)
	// UploadPart 服务端代传单个分片，返回该片 ETag。
	UploadPart(ctx context.Context, key, uploadID string, number int32, r io.Reader, size int64) (etag string, err error)
	// ListParts 列出已上传分片，是断点续传的「真相源」。
	ListParts(ctx context.Context, key, uploadID string) ([]Part, error)
	// CompleteMultipart 按序号升序合并全部分片。
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error
	// AbortMultipart 放弃上传并释放分片，用于取消与过期清理。
	AbortMultipart(ctx context.Context, key, uploadID string) error
}

// New 依据上传配置构造 Storage。
//
// 与 mailer.New 一样采用「工厂 + 驱动名」的形状，新增驱动只需在这里加一个分支。
func New(cfg config.UploadConfig) (Storage, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Driver)) {
	case "", DriverLocal:
		return NewLocal(cfg.LocalDir), nil
	case DriverMinIO:
		return NewMinIO(cfg.MinIO)
	default:
		return nil, fmt.Errorf("不支持的 upload.driver: %s", cfg.Driver)
	}
}
