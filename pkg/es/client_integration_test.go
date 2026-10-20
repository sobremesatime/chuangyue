//go:build integration

// 检索索引客户端的集成测试：需要本地 Elasticsearch（默认 127.0.0.1:9200）。
//
//	go test -tags integration -v ./pkg/es/...
//	# 端口不同时：ES_ADDR=http://127.0.0.1:9201 go test -tags integration ./pkg/es/...
//
// 测试使用带时间戳的临时索引，结束时删除，不会影响开发环境的 chuangyue_videos。
package es

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
)

// newTestClient 连本地 ES；不可用时跳过而不是失败。
func newTestClient(t *testing.T) *Client {
	t.Helper()

	addr := os.Getenv("ES_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:9200"
	}

	client, err := New(Config{
		Addresses: []string{addr},
		Index:     fmt.Sprintf("chuangyue_it_%d", time.Now().UnixNano()),
		Timeout:   3 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造 ES 客户端失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.EnsureIndex(ctx); err != nil {
		t.Skipf("Elasticsearch 不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.DeleteIndex(cleanupCtx)
	})
	return client
}

// TestClientVideoIndex 覆盖建索引、写入、中文短语检索、分类过滤、排序与删除。
func TestClientVideoIndex(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	marker := fmt.Sprintf("%d", time.Now().UnixNano())
	const (
		categoryA int64 = 991
		categoryB int64 = 992
	)

	// 两条标题都含 marker；用创建时间区分先后，验证默认按最新倒序。
	older := videodto.VideoIndexDoc{
		ID:          7001,
		Title:       "窗月索引测试 " + marker,
		Description: "关于窗月的搜索结果",
		CategoryID:  categoryA,
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	newer := videodto.VideoIndexDoc{
		ID:          7002,
		Title:       "窗月索引测试 " + marker,
		Description: "另一条结果",
		CategoryID:  categoryB,
		CreatedAt:   time.Now(),
	}
	for _, doc := range []videodto.VideoIndexDoc{older, newer} {
		if err := client.Index(ctx, fmt.Sprintf("%d", doc.ID), doc); err != nil {
			t.Fatalf("写入索引失败: %v", err)
		}
	}
	if err := client.Refresh(ctx); err != nil {
		t.Fatalf("刷新索引失败: %v", err)
	}

	count, err := client.Count(ctx)
	if err != nil || count != 2 {
		t.Fatalf("索引文档数应为 2, 实际 count=%d err=%v", count, err)
	}

	// ① 中文短语命中：关键词必须是连续片段，且按创建时间倒序。
	t.Run("chinese_phrase_and_order", func(t *testing.T) {
		ids, total, err := client.SearchIDs(ctx, "窗月索引", 0, 0, 10)
		if err != nil {
			t.Fatalf("检索失败: %v", err)
		}
		if total != 2 || len(ids) != 2 {
			t.Fatalf("应命中 2 条, 实际 total=%d len=%d", total, len(ids))
		}
		if ids[0] != newer.ID || ids[1] != older.ID {
			t.Fatalf("应按创建时间倒序, 实际 %v", ids)
		}
	})

	// ② 不连续的中文片段不应命中（phrase 语义）。
	t.Run("phrase_precision", func(t *testing.T) {
		_, total, err := client.SearchIDs(ctx, "窗月结果", 0, 0, 10)
		if err != nil {
			t.Fatalf("检索失败: %v", err)
		}
		if total != 0 {
			t.Fatalf("不连续短语不应命中, 实际 total=%d", total)
		}
	})

	// ③ 分类过滤与关键词是「与」关系。
	t.Run("category_filter", func(t *testing.T) {
		ids, total, err := client.SearchIDs(ctx, "窗月索引", categoryA, 0, 10)
		if err != nil {
			t.Fatalf("检索失败: %v", err)
		}
		if total != 1 || len(ids) != 1 || ids[0] != older.ID {
			t.Fatalf("分类过滤结果不对: total=%d ids=%v", total, ids)
		}
	})

	// ④ 分页：from/size 生效，total 仍是命中总数。
	t.Run("pagination", func(t *testing.T) {
		ids, total, err := client.SearchIDs(ctx, "窗月索引", 0, 1, 1)
		if err != nil {
			t.Fatalf("检索失败: %v", err)
		}
		if total != 2 || len(ids) != 1 || ids[0] != older.ID {
			t.Fatalf("分页结果不对: total=%d ids=%v", total, ids)
		}
	})

	// ⑤ 删除文档（含重复删除）后不再命中。
	t.Run("delete_is_idempotent", func(t *testing.T) {
		if err := client.Delete(ctx, fmt.Sprintf("%d", older.ID)); err != nil {
			t.Fatalf("删除索引文档失败: %v", err)
		}
		if err := client.Delete(ctx, fmt.Sprintf("%d", older.ID)); err != nil {
			t.Fatalf("重复删除应视为成功: %v", err)
		}
		if err := client.Refresh(ctx); err != nil {
			t.Fatalf("刷新索引失败: %v", err)
		}

		ids, total, err := client.SearchIDs(ctx, "窗月索引", 0, 0, 10)
		if err != nil {
			t.Fatalf("检索失败: %v", err)
		}
		if total != 1 || len(ids) != 1 || ids[0] != newer.ID {
			t.Fatalf("删除后应只剩新文档: total=%d ids=%v", total, ids)
		}
	})
}
