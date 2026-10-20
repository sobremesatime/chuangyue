//go:build integration

// 视频服务 gRPC 读路径的集成测试：需要本地 MySQL。
//
// 覆盖新增的 ListVideos（分页 total、分类过滤、关键词）与 IncrViewCount（存在 / 不存在），
// 依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/video/handler/...
package handler

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
)

// publishedVideo 取一条已上架视频用于测试，库中不存在则返回 nil。
func publishedVideo(t *testing.T, db *gorm.DB) *videomodel.Video {
	t.Helper()

	var v videomodel.Video
	err := db.Where("status = ?", videomodel.StatusPublished).Order("id ASC").First(&v).Error
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		t.Fatalf("查询已上架视频失败: %v", err)
	}
	return &v
}

// TestGRPCListVideos 覆盖列表：分页 total 与窗口、参数归一、分类过滤、关键词。
func TestGRPCListVideos(t *testing.T) {
	db := newTestDB(t)
	srv := NewGRPCVideoServer(service.New(videorepo.New(db), nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, nil))
	ctx := context.Background()

	pub := publishedVideo(t, db)
	if pub == nil {
		t.Skip("库中没有已上架视频，跳过 ListVideos 验证")
	}

	// total 与库内已上架计数一致，且 page_size=1 时窗口不超过 1 条。
	var wantTotal int64
	if err := db.Model(&videomodel.Video{}).Where("status = ?", videomodel.StatusPublished).Count(&wantTotal).Error; err != nil {
		t.Fatalf("统计已上架视频失败: %v", err)
	}
	resp, err := srv.ListVideos(ctx, &videov1.ListVideosRequest{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("ListVideos 失败: %v", err)
	}
	if resp.GetTotal() != wantTotal {
		t.Fatalf("ListVideos total=%d, 期望 %d", resp.GetTotal(), wantTotal)
	}
	if len(resp.GetVideos()) > 1 {
		t.Fatalf("page_size=1 返回了 %d 条", len(resp.GetVideos()))
	}

	// 参数归一：非法分页应回落默认值而非报错。
	if _, err := srv.ListVideos(ctx, &videov1.ListVideosRequest{Page: 0, PageSize: 0}); err != nil {
		t.Fatalf("非法分页 ListVideos 失败: %v", err)
	}

	// 分类过滤：返回项的分类应全部等于所查分类，且包含目标视频。
	byCat, err := srv.ListVideos(ctx, &videov1.ListVideosRequest{CategoryId: pub.CategoryID, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("分类过滤 ListVideos 失败: %v", err)
	}
	catHit := false
	for _, v := range byCat.GetVideos() {
		if v.GetCategoryId() != pub.CategoryID {
			t.Fatalf("分类过滤返回了其它分类的视频: %d", v.GetCategoryId())
		}
		if v.GetId() == pub.ID {
			catHit = true
		}
	}
	if !catHit {
		t.Fatalf("分类过滤未命中目标视频 category_id=%d", pub.CategoryID)
	}

	// 关键词：用目标视频标题搜索，应命中该视频。
	byKw, err := srv.ListVideos(ctx, &videov1.ListVideosRequest{Keyword: pub.Title, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("关键词 ListVideos 失败: %v", err)
	}
	kwHit := false
	for _, v := range byKw.GetVideos() {
		if v.GetId() == pub.ID {
			kwHit = true
		}
	}
	if !kwHit {
		t.Fatalf("关键词过滤未命中目标视频 title=%q", pub.Title)
	}
}

// TestGRPCIncrViewCount 覆盖播放量自增：存在则 +1，不存在返回 NotFound。
func TestGRPCIncrViewCount(t *testing.T) {
	db := newTestDB(t)
	repo := videorepo.New(db)
	srv := NewGRPCVideoServer(service.New(repo, nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, nil))
	ctx := context.Background()

	pub := publishedVideo(t, db)
	if pub == nil {
		t.Skip("库中没有已上架视频，跳过 IncrViewCount 验证")
	}

	before := pub.ViewCount
	if _, err := srv.IncrViewCount(ctx, &videov1.IncrViewCountRequest{VideoId: pub.ID}); err != nil {
		t.Fatalf("IncrViewCount 失败: %v", err)
	}
	after, err := repo.GetByID(ctx, pub.ID)
	if err != nil {
		t.Fatalf("自增后直查失败: %v", err)
	}
	if after.ViewCount != before+1 {
		t.Fatalf("view_count %d -> %d, 期望 %d", before, after.ViewCount, before+1)
	}

	// 视频不存在时必须返回 NotFound。
	if _, err := srv.IncrViewCount(ctx, &videov1.IncrViewCountRequest{VideoId: 1 << 62}); status.Code(err) != codes.NotFound {
		t.Fatalf("不存在视频应返回 NotFound, 实际: %v", err)
	}
}
