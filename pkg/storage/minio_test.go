package storage

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/sobremesatime/chuangyue/pkg/config"
)

// TestMinIOURLRoundTrip 校验对外地址与对象 key 的互转。
//
// 直接构造结构体字面量（core 留空），避免依赖真实的对象存储。
func TestMinIOURLRoundTrip(t *testing.T) {
	m := &minioStorage{bucket: "chuangyue", publicURL: "http://cdn.example.com/chuangyue"}

	const key = "video/source/abc.mp4"
	const want = "http://cdn.example.com/chuangyue/video/source/abc.mp4"
	if got := m.URL(key); got != want {
		t.Fatalf("URL() = %q, want %q", got, want)
	}

	got, ok := m.KeyFromURL(m.URL(key))
	if !ok || got != key {
		t.Fatalf("KeyFromURL() = (%q, %v), want (%q, true)", got, ok, key)
	}

	if _, ok := m.KeyFromURL("/uploads/video/source/abc.mp4"); ok {
		t.Fatal("KeyFromURL() 不应识别其它驱动的地址")
	}
}

// TestMinIOBuildPublicURL 校验对外访问前缀的推导规则。
func TestMinIOBuildPublicURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.MinIOConfig
		want string
	}{
		{
			name: "显式配置优先并去掉尾部斜杠",
			cfg:  config.MinIOConfig{Endpoint: "127.0.0.1:9000", Bucket: "b", PublicURL: "http://cdn.example.com/b/"},
			want: "http://cdn.example.com/b",
		},
		{
			name: "未配置时按 endpoint 推导",
			cfg:  config.MinIOConfig{Endpoint: "127.0.0.1:9000", Bucket: "chuangyue"},
			want: "http://127.0.0.1:9000/chuangyue",
		},
		{
			name: "use_ssl 推导为 https",
			cfg:  config.MinIOConfig{Endpoint: "minio.example.com", Bucket: "b", UseSSL: true},
			want: "https://minio.example.com/b",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildPublicURL(c.cfg); got != c.want {
				t.Fatalf("buildPublicURL() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestLocalURLRoundTripAndOpen 顺带覆盖 local 驱动的地址反解与读取。
func TestLocalURLRoundTripAndOpen(t *testing.T) {
	l := NewLocal(t.TempDir())
	ctx := context.Background()

	const key = "video/source/abc.mp4"
	content := []byte("hello")
	if err := l.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "video/mp4"); err != nil {
		t.Fatalf("Put() 失败: %v", err)
	}

	got, ok := l.KeyFromURL(l.URL(key))
	if !ok || got != key {
		t.Fatalf("KeyFromURL() = (%q, %v), want (%q, true)", got, ok, key)
	}

	rc, err := l.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer rc.Close()

	read, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(read, content) {
		t.Fatalf("读到 %q, want %q", read, content)
	}
}
