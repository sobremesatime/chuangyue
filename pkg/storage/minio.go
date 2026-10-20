package storage

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/sobremesatime/chuangyue/pkg/config"
)

// minioListMaxParts 是一次 ListObjectParts 请求的分页大小。
const minioListMaxParts = 1000

// normalizeETag 去掉 ETag 两侧的引号，统一为不带引号的十六进制串。
//
// S3 的 PutObjectPart 返回带引号的 ETag（如 "344d…"），ListObjectParts 返回的却是
// 不带引号的同名值；local 驱动返回的也是不带引号的 MD5。统一后落库与再次提交
// 都不必关心 ETag 来自哪个接口。
func normalizeETag(etag string) string {
	return strings.Trim(etag, `"`)
}

// minioStorage 是落对象存储（S3 协议）的驱动实现。
//
// 它同时实现 Storage 与 MultipartUploader：单次写入走 PutObject，
// 分片上传交给对象存储的 Multipart API，合并由服务端完成，
// 因此不必像 local 驱动那样自己拼接分片。
//
// 用 minio.Core 而非 minio.Client：Core 内嵌 *Client，一个对象即可同时调用
// 高层 API 与分片相关低层 API（NewMultipartUpload / PutObjectPart 等）。
type minioStorage struct {
	core      *minio.Core
	bucket    string
	publicURL string
}

// NewMinIO 构造对象存储驱动，返回 Storage 接口。
//
// 构造时确认桶存在（不存在则创建），让配置问题在启动期暴露，
// 而不是等到第一次上传才报错。桶的公共读策略不在此处设置：
// 由部署方按需授予匿名只读，或通过 CDN 暴露（见 public_url）。
func NewMinIO(cfg config.MinIOConfig) (Storage, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("upload.minio.endpoint 未配置")
	}

	core, err := minio.NewCore(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化对象存储客户端失败: %w", err)
	}

	m := &minioStorage{core: core, bucket: cfg.Bucket, publicURL: buildPublicURL(cfg)}
	if err := m.ensureBucket(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

// buildPublicURL 计算对象的对外访问前缀：优先用显式配置，否则按 endpoint 推导。
func buildPublicURL(cfg config.MinIOConfig) string {
	if v := strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/"); v != "" {
		return v
	}
	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/%s", scheme, strings.TrimSpace(cfg.Endpoint), cfg.Bucket)
}

// ensureBucket 确认桶存在，不存在则创建。
func (m *minioStorage) ensureBucket(ctx context.Context) error {
	ok, err := m.core.BucketExists(ctx, m.bucket)
	if err != nil {
		return fmt.Errorf("检查存储桶失败: %w", err)
	}
	if ok {
		return nil
	}
	if err := m.core.MakeBucket(ctx, m.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("创建存储桶 %s 失败: %w", m.bucket, err)
	}
	return nil
}

// Driver 实现 Storage。
func (m *minioStorage) Driver() string { return DriverMinIO }

// Put 实现 Storage：一次性写入对象。
func (m *minioStorage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if _, err := m.core.PutObject(ctx, m.bucket, key, r, size, "", "", minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("写入对象失败: %w", err)
	}
	return nil
}

// Delete 实现 Storage：删除对象，对象不存在时同样视为成功。
func (m *minioStorage) Delete(ctx context.Context, key string) error {
	if err := m.core.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("删除对象失败: %w", err)
	}
	return nil
}

// Open 实现 Storage：打开对象用于读取。
func (m *minioStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, _, _, err := m.core.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("打开对象失败: %w", err)
	}
	return obj, nil
}

// URL 实现 Storage：返回对象的公共直链（无有效期，可直接入库）。
func (m *minioStorage) URL(key string) string {
	return m.publicURL + "/" + strings.TrimLeft(key, "/")
}

// KeyFromURL 实现 Storage：剥掉公共前缀即为对象 key。
func (m *minioStorage) KeyFromURL(url string) (string, bool) {
	prefix := m.publicURL + "/"
	if !strings.HasPrefix(url, prefix) {
		return "", false
	}
	return strings.TrimPrefix(url, prefix), true
}

// PresignPut 实现 Storage：返回单次直传的预签名地址。
func (m *minioStorage) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := m.core.PresignedPutObject(ctx, m.bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("生成预签名地址失败: %w", err)
	}
	return u.String(), nil
}

// Multipart 实现 Storage：对象存储原生支持分片上传。
func (m *minioStorage) Multipart() (MultipartUploader, error) { return m, nil }

// InitMultipart 实现 MultipartUploader：发起服务端分片上传。
func (m *minioStorage) InitMultipart(ctx context.Context, key, contentType string) (string, error) {
	uploadID, err := m.core.NewMultipartUpload(ctx, m.bucket, key, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("发起分片上传失败: %w", err)
	}
	return uploadID, nil
}

// PresignPart 实现 MultipartUploader。
//
// 对象存储本身支持分片级预签名，但当前前端统一走网关代传（putPart），
// 因此这里暂不提供，待「前端预签名直传」一并实现。
func (m *minioStorage) PresignPart(_ context.Context, _ string, _ string, _ int32, _ time.Duration) (string, error) {
	return "", ErrNotSupported
}

// UploadPart 实现 MultipartUploader：上传单个分片并返回该片 ETag。
func (m *minioStorage) UploadPart(ctx context.Context, key, uploadID string, number int32, r io.Reader, size int64) (string, error) {
	part, err := m.core.PutObjectPart(ctx, m.bucket, key, uploadID, int(number), r, size, minio.PutObjectPartOptions{})
	if err != nil {
		return "", fmt.Errorf("上传分片失败: %w", err)
	}
	return normalizeETag(part.ETag), nil
}

// ListParts 实现 MultipartUploader：分页取回已上传分片，按序号升序返回。
func (m *minioStorage) ListParts(ctx context.Context, key, uploadID string) ([]Part, error) {
	var parts []Part
	marker := 0
	for {
		res, err := m.core.ListObjectParts(ctx, m.bucket, key, uploadID, marker, minioListMaxParts)
		if err != nil {
			return nil, fmt.Errorf("查询已上传分片失败: %w", err)
		}
		for _, p := range res.ObjectParts {
			parts = append(parts, Part{Number: int32(p.PartNumber), ETag: normalizeETag(p.ETag), Size: p.Size})
		}
		if !res.IsTruncated {
			break
		}
		marker = res.NextPartNumberMarker
	}

	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	return parts, nil
}

// CompleteMultipart 实现 MultipartUploader：按序号升序合并全部分片。
func (m *minioStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error {
	ordered := make([]Part, len(parts))
	copy(ordered, parts)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })

	complete := make([]minio.CompletePart, 0, len(ordered))
	for _, p := range ordered {
		complete = append(complete, minio.CompletePart{PartNumber: int(p.Number), ETag: normalizeETag(p.ETag)})
	}
	if _, err := m.core.CompleteMultipartUpload(ctx, m.bucket, key, uploadID, complete, minio.PutObjectOptions{}); err != nil {
		return fmt.Errorf("合并分片失败: %w", err)
	}
	return nil
}

// AbortMultipart 实现 MultipartUploader：放弃上传并释放服务端分片。
func (m *minioStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if err := m.core.AbortMultipartUpload(ctx, m.bucket, key, uploadID); err != nil {
		return fmt.Errorf("放弃分片上传失败: %w", err)
	}
	return nil
}
