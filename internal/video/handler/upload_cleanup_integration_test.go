//go:build integration

// 过期上传清理（周期任务入口 CleanupUploads）的集成测试：需要本地 MySQL，不可用则跳过。
//
//	go test -tags integration -v -run TestCleanupUploads ./internal/video/handler/...
package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/storage"
)

// cleanupTestSessionID 是测试专用的会话 ID：取一个不会与雪花 ID 撞车的固定值，便于前置清理。
const cleanupTestSessionID int64 = 42424242424242

// TestCleanupUploadsReclaimsExpiredSession 覆盖周期任务入口：一轮清理应回收已过期的上传会话。
func TestCleanupUploadsReclaimsExpiredSession(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	repo := videorepo.New(db)
	// 本地磁盘驱动：清理会先向驱动释放临时分片，store 不能为 nil。
	// rdb 传 nil：不接 Redis 时清理不做单飞，退化为「每轮都跑」，正是要验证的默认路径。
	svc := service.New(repo, storage.NewLocal(t.TempDir()),
		config.UploadConfig{LocalDir: t.TempDir()}, config.TranscodeConfig{}, nil, nil)

	if err := db.Where("id = ?", cleanupTestSessionID).Delete(&videomodel.UploadSession{}).Error; err != nil {
		t.Fatalf("前置清理测试会话失败: %v", err)
	}
	t.Cleanup(func() { db.Where("id = ?", cleanupTestSessionID).Delete(&videomodel.UploadSession{}) })

	sess := videomodel.UploadSession{
		ID:         cleanupTestSessionID,
		UserID:     1,
		FileMD5:    strings.Repeat("a", 32),
		Filename:   "cleanup-it.mp4",
		TotalSize:  1024,
		ChunkSize:  512,
		PartTotal:  2,
		StorageKey: "it/cleanup",
		Driver:     storage.DriverLocal,
		Status:     videomodel.UploadStatusUploading,
		ExpiresAt:  time.Now().Add(-time.Hour), // 已过期，应被回收
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("插入过期上传会话失败: %v", err)
	}

	svc.CleanupUploads(ctx)

	var got videomodel.UploadSession
	err := db.Where("id = ?", cleanupTestSessionID).First(&got).Error
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("过期会话应被清理，却仍存在于库中: err=%v", err)
	}
}
