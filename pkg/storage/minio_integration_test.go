//go:build integration

// 对象存储驱动的集成测试：需要本地 MinIO。
//
// 默认不参与 `go test ./...`（用 integration 构建标签隔离），
// 需要真实对象存储时显式执行：
//
//	docker compose -f deploy/compose/docker-compose.yml --profile full up -d minio
//	go test -tags integration -v -run TestMinIO ./pkg/storage/...
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sobremesatime/chuangyue/pkg/config"
)

// minPartSize 是 S3 对「非末片」分片的最小要求。
const minPartSize = 5 << 20

// newTestStorage 依据环境变量连到本地 MinIO，默认值对应 compose 里的配置。
func newTestStorage(t *testing.T) Storage {
	t.Helper()

	cfg := config.MinIOConfig{
		Endpoint:  envOr("MINIO_ENDPOINT", "127.0.0.1:9000"),
		AccessKey: envOr("MINIO_ACCESS_KEY", "minioadmin"),
		SecretKey: envOr("MINIO_SECRET_KEY", "minioadmin"),
		Bucket:    envOr("MINIO_BUCKET", "chuangyue-it"),
		Region:    envOr("MINIO_REGION", "us-east-1"),
	}

	store, err := NewMinIO(cfg)
	if err != nil {
		t.Fatalf("连接 MinIO 失败（请先启动容器：docker compose -f deploy/compose/docker-compose.yml --profile full up -d minio）: %v", err)
	}
	return store
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// uniqueKey 生成带时间戳的唯一对象 key，避免历史残留互相干扰。
func uniqueKey(prefix, ext string) string {
	return fmt.Sprintf("%s/it-%d%s", prefix, time.Now().UnixNano(), ext)
}

// readAll 读取对象全部内容。
func readAll(t *testing.T, ctx context.Context, store Storage, key string) []byte {
	t.Helper()

	rc, err := store.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open(%s) 失败: %v", key, err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", key, err)
	}
	return data
}

// TestMinIOObjectRoundTrip 覆盖单次写入、读取、地址反解与删除。
func TestMinIOObjectRoundTrip(t *testing.T) {
	store := newTestStorage(t)
	ctx := context.Background()

	key := uniqueKey("video/source", ".mp4")
	content := []byte("hello minio")
	if err := store.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "video/mp4"); err != nil {
		t.Fatalf("Put() 失败: %v", err)
	}

	if got, ok := store.KeyFromURL(store.URL(key)); !ok || got != key {
		t.Fatalf("KeyFromURL(URL(key)) = (%q, %v), want (%q, true)", got, ok, key)
	}
	if got := readAll(t, ctx, store, key); !bytes.Equal(got, content) {
		t.Fatalf("Open() 读到 %q, want %q", got, content)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete() 失败: %v", err)
	}
	// GetObject 是惰性的：可能在 Open 时就报错，也可能到 Read 才报错
	if rc, err := store.Open(ctx, key); err == nil {
		_, rerr := io.ReadAll(rc)
		_ = rc.Close()
		if rerr == nil {
			t.Fatal("对象删除后仍能读到内容")
		}
	}
}

// TestMinIOMultipart 覆盖完整分片链路：乱序上传 → 列出 → 乱序提交 → 回读。
func TestMinIOMultipart(t *testing.T) {
	store := newTestStorage(t)
	ctx := context.Background()

	up, err := store.Multipart()
	if err != nil {
		t.Fatalf("Multipart() 失败: %v", err)
	}

	key := uniqueKey("video/source", ".mp4")
	parts := [][]byte{
		bytes.Repeat([]byte{0xAA}, minPartSize),
		bytes.Repeat([]byte{0xBB}, minPartSize),
		bytes.Repeat([]byte{0xCC}, 64<<10), // 末片允许小于最小分片
	}

	uploadID, err := up.InitMultipart(ctx, key, "video/mp4")
	if err != nil {
		t.Fatalf("InitMultipart() 失败: %v", err)
	}
	// 无论成败都释放服务端分片，避免残留
	t.Cleanup(func() { _ = up.AbortMultipart(context.Background(), key, uploadID) })
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	// 乱序上传：3、1、2
	uploaded := make(map[int32]Part, len(parts))
	for _, n := range []int{3, 1, 2} {
		body := parts[n-1]
		etag, err := up.UploadPart(ctx, key, uploadID, int32(n), bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("UploadPart(%d) 失败: %v", n, err)
		}
		if etag == "" {
			t.Fatalf("UploadPart(%d) 未返回 ETag", n)
		}
		uploaded[int32(n)] = Part{Number: int32(n), ETag: etag, Size: int64(len(body))}
	}

	listed, err := up.ListParts(ctx, key, uploadID)
	if err != nil {
		t.Fatalf("ListParts() 失败: %v", err)
	}
	if len(listed) != len(parts) {
		t.Fatalf("ListParts() 返回 %d 片, want %d", len(listed), len(parts))
	}
	for i, p := range listed {
		want := int32(i + 1)
		if p.Number != want {
			t.Fatalf("ListParts()[%d].Number = %d, want %d（应按序号升序）", i, p.Number, want)
		}
		// 两个接口给出的 ETag 必须一致（驱动已统一去掉引号）
		if p.ETag != uploaded[want].ETag || p.Size != uploaded[want].Size {
			t.Fatalf("ListParts()[%d] = %+v, want %+v", i, p, uploaded[want])
		}
		if strings.Contains(p.ETag, `"`) {
			t.Fatalf("ETag 不应带引号: %q", p.ETag)
		}
	}

	// 用 UploadPart 返回的 ETag 提交（与生产链路一致），并故意打乱顺序
	if err := up.CompleteMultipart(ctx, key, uploadID,
		[]Part{uploaded[2], uploaded[1], uploaded[3]}); err != nil {
		t.Fatalf("CompleteMultipart() 失败: %v", err)
	}

	want := bytes.Join(parts, nil)
	if got := readAll(t, ctx, store, key); !bytes.Equal(got, want) {
		t.Fatalf("合并后对象大小 %d, want %d", len(got), len(want))
	}
}

// TestMinIOMultipartAbort 覆盖放弃上传：分片被释放，uploadID 随即失效。
func TestMinIOMultipartAbort(t *testing.T) {
	store := newTestStorage(t)
	ctx := context.Background()

	up, err := store.Multipart()
	if err != nil {
		t.Fatalf("Multipart() 失败: %v", err)
	}

	key := uniqueKey("video/source", ".abort.mp4")
	uploadID, err := up.InitMultipart(ctx, key, "video/mp4")
	if err != nil {
		t.Fatalf("InitMultipart() 失败: %v", err)
	}

	body := bytes.Repeat([]byte{0x11}, minPartSize)
	if _, err := up.UploadPart(ctx, key, uploadID, 1, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("UploadPart() 失败: %v", err)
	}

	if err := up.AbortMultipart(ctx, key, uploadID); err != nil {
		t.Fatalf("AbortMultipart() 失败: %v", err)
	}
	if _, err := up.ListParts(ctx, key, uploadID); err == nil {
		t.Fatal("AbortMultipart() 后 uploadID 仍可用")
	}
}
