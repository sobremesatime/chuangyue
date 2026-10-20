package storage

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// localURLPrefix 是本地静态资源的访问前缀，与网关静态目录挂载保持一致。
//
// 转码 Worker 也按这个前缀把源文件地址还原为本地路径，两端必须一致。
const localURLPrefix = "/uploads/"

// localTmpDir 是分片临时目录名，位于上传根目录下。
//
// 合并完成后整体删除；进程异常退出遗留的目录由清理任务按会话过期时间回收。
const localTmpDir = "tmp"

// localStorage 是落本地磁盘的驱动实现。
//
// 它同时实现 Storage 与 MultipartUploader：分片先写入临时目录，
// 合并时按序号拼接成最终文件。这样「合并由谁做」的差异被封装在驱动内，
// 业务层切换 local / minio 时无需改动。
type localStorage struct {
	root string
}

// NewLocal 构造本地磁盘驱动，root 为上传根目录。
func NewLocal(root string) *localStorage {
	return &localStorage{root: root}
}

// Driver 实现 Storage。
func (l *localStorage) Driver() string { return DriverLocal }

// path 把对象 key 还原为本地磁盘路径。
//
// key 统一用正斜杠（跨平台），落盘时再转成当前系统的分隔符。
func (l *localStorage) path(key string) string {
	return filepath.Join(l.root, filepath.FromSlash(key))
}

// partDir 返回某个分片会话的临时目录。
func (l *localStorage) partDir(uploadID string) string {
	return filepath.Join(l.root, localTmpDir, uploadID)
}

// Put 实现 Storage：一次性写入对象。
func (l *localStorage) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	dst := l.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建对象目录失败: %w", err)
	}

	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建对象文件失败: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("写入对象失败: %w", err)
	}
	return nil
}

// Delete 实现 Storage：删除对象，文件本就不存在时视为成功。
func (l *localStorage) Delete(_ context.Context, key string) error {
	if err := os.Remove(l.path(key)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// URL 实现 Storage：拼出对外可访问地址。
func (l *localStorage) URL(key string) string {
	return localURLPrefix + filepath.ToSlash(key)
}

// Open 实现 Storage：打开本地文件用于读取。
func (l *localStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	return os.Open(l.path(key))
}

// KeyFromURL 实现 Storage：剥掉 /uploads/ 前缀即为对象 key。
func (l *localStorage) KeyFromURL(url string) (string, bool) {
	if !strings.HasPrefix(url, localURLPrefix) {
		return "", false
	}
	return strings.TrimPrefix(url, localURLPrefix), true
}

// PresignPut 实现 Storage：本地磁盘不提供预签名直传。
func (l *localStorage) PresignPut(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", ErrNotSupported
}

// Multipart 实现 Storage：本地驱动支持分片上传（自行合并）。
func (l *localStorage) Multipart() (MultipartUploader, error) { return l, nil }

// InitMultipart 实现 MultipartUploader：分配一个 token 并建好临时目录。
func (l *localStorage) InitMultipart(_ context.Context, _ string, _ string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(l.partDir(token), 0o755); err != nil {
		return "", fmt.Errorf("创建分片临时目录失败: %w", err)
	}
	return token, nil
}

// PresignPart 实现 MultipartUploader：本地磁盘不提供预签名直传。
func (l *localStorage) PresignPart(_ context.Context, _ string, _ string, _ int32, _ time.Duration) (string, error) {
	return "", ErrNotSupported
}

// UploadPart 实现 MultipartUploader：写单片刻盘，并返回该片的 MD5 作为 ETag。
func (l *localStorage) UploadPart(_ context.Context, _ string, uploadID string, number int32, r io.Reader, _ int64) (string, error) {
	dst := filepath.Join(l.partDir(uploadID), strconv.FormatInt(int64(number), 10))

	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("创建分片文件失败: %w", err)
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("写入分片失败: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ListParts 实现 MultipartUploader：扫描临时目录，返回已上传的分片。
//
// ETag 按需重算（读取文件算 MD5），因为临时目录不额外维护索引；
// 该方法只在断点续传与收尾时调用，频率低，重算可接受。
func (l *localStorage) ListParts(_ context.Context, _ string, uploadID string) ([]Part, error) {
	entries, err := os.ReadDir(l.partDir(uploadID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取分片目录失败: %w", err)
	}

	parts := make([]Part, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		number, err := strconv.ParseInt(entry.Name(), 10, 32)
		if err != nil || number < 1 {
			continue
		}
		full := filepath.Join(l.partDir(uploadID), entry.Name())
		size, etag, err := fileMD5(full)
		if err != nil {
			return nil, err
		}
		parts = append(parts, Part{Number: int32(number), ETag: etag, Size: size})
	}

	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	return parts, nil
}

// CompleteMultipart 实现 MultipartUploader：按序号升序拼接成分片为最终对象。
//
// 入参 parts 允许乱序，驱动内部会先排序；因此调用方不必关心提交顺序。
func (l *localStorage) CompleteMultipart(_ context.Context, key, uploadID string, parts []Part) error {
	ordered := make([]Part, len(parts))
	copy(ordered, parts)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })

	dst := l.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建对象目录失败: %w", err)
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建对象文件失败: %w", err)
	}

	for _, p := range ordered {
		src := filepath.Join(l.partDir(uploadID), strconv.FormatInt(int64(p.Number), 10))
		in, err := os.Open(src)
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dst)
			return fmt.Errorf("读取分片 %d 失败: %w", p.Number, err)
		}
		_, copyErr := io.Copy(out, in)
		_ = in.Close()
		if copyErr != nil {
			_ = out.Close()
			_ = os.Remove(dst)
			return fmt.Errorf("合并分片 %d 失败: %w", p.Number, copyErr)
		}
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("关闭对象文件失败: %w", err)
	}
	// 合并成功后临时分片不再需要。
	_ = os.RemoveAll(l.partDir(uploadID))
	return nil
}

// AbortMultipart 实现 MultipartUploader：丢弃全部临时分片。
func (l *localStorage) AbortMultipart(_ context.Context, _ string, uploadID string) error {
	if err := os.RemoveAll(l.partDir(uploadID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// fileMD5 返回文件的大小与 MD5（hex 小写）。
func fileMD5(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("打开分片失败: %w", err)
	}
	defer f.Close()

	h := md5.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return 0, "", fmt.Errorf("读取分片失败: %w", err)
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// randomToken 生成一个随机上传会话 token，用于隔离不同上传的临时目录。
func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成上传令牌失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
