//go:build integration

// 「内容被管理员删除时通知作者」的集成测试：需要本地 MySQL（互动库）。
//
//	go test -tags integration -v -run TestAdminDeleteNotifiesAuthor ./internal/interaction/handler/...
package handler

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
	interactionrepo "github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
)

func TestAdminDeleteNotifiesAuthor(t *testing.T) {
	db := newInteractionTestDB(t)
	repo := interactionrepo.New(db)
	ctx := context.Background()

	// 通知服务不依赖 users / videos 端口就能写入（ContentRemoved 只落库）
	notifier := service.NewNotificationService(repo, nil, nil)
	comments := service.NewCommentService(repo, notifier, nil, nil)
	danmaku := service.NewDanmakuService(repo, notifier, nil, nil)

	const (
		videoID   = int64(8000000000000001001)
		commentID = int64(8000000000000001002)
		danmakuID = int64(8000000000000001003)
		authorID  = int64(8000000000000001004)
		adminID   = int64(8000000000000001005)
	)

	cleanup := func() {
		for _, m := range []any{
			&model.VideoComment{}, &model.VideoDanmaku{}, &model.Notification{},
		} {
			_ = db.Where("video_id = ?", videoID).Delete(m).Error
		}
		_ = db.Where("id IN ?", []int64{commentID, danmakuID}).Delete(&model.VideoComment{}).Error
		_ = db.Where("id = ?", danmakuID).Delete(&model.VideoDanmaku{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := db.Create(&model.VideoComment{
		ID: commentID, VideoID: videoID, UserID: authorID,
		Content: "这条评论会被删", Status: model.CommentStatusNormal,
	}).Error; err != nil {
		t.Fatalf("准备评论失败: %v", err)
	}
	if err := db.Create(&model.VideoDanmaku{
		ID: danmakuID, VideoID: videoID, UserID: authorID, Content: "这条弹幕会被删",
		TimeMs: 1000, Color: "#FFFFFF", Mode: model.DanmakuModeScroll, Status: model.DanmakuStatusNormal,
	}).Error; err != nil {
		t.Fatalf("准备弹幕失败: %v", err)
	}

	// -------- ① 管理员删评论：作者收到 type=4 的通知，且带原文快照 --------
	if err := comments.AdminDelete(ctx, adminID, commentID); err != nil {
		t.Fatalf("删除评论失败: %v", err)
	}
	var commentNotify model.Notification
	if err := db.Where("user_id = ? AND type = ?", authorID, model.NotifyTypeCommentRemoved).
		First(&commentNotify).Error; err != nil {
		t.Fatalf("应写入「评论被删除」通知，实际 err=%v", err)
	}
	if commentNotify.ActorID != adminID || commentNotify.TargetID != commentID ||
		commentNotify.VideoID != videoID {
		t.Fatalf("通知字段不符: %+v", commentNotify)
	}
	if !strings.Contains(commentNotify.Content, "会被删") {
		t.Fatalf("通知应带被删内容快照，实际 %q", commentNotify.Content)
	}

	// 评论本身是软删：行还在，状态置为已删除
	var deletedComment model.VideoComment
	if err := db.First(&deletedComment, commentID).Error; err != nil {
		t.Fatalf("回查评论失败: %v", err)
	}
	if deletedComment.Status != model.CommentStatusDeleted {
		t.Fatalf("评论应为软删状态，实际 %d", deletedComment.Status)
	}

	// -------- ② 管理员删弹幕：作者收到 type=5 --------
	if err := danmaku.AdminDelete(ctx, adminID, danmakuID); err != nil {
		t.Fatalf("删除弹幕失败: %v", err)
	}
	var danmakuNotify model.Notification
	if err := db.Where("user_id = ? AND type = ?", authorID, model.NotifyTypeDanmakuRemoved).
		First(&danmakuNotify).Error; err != nil {
		t.Fatalf("应写入「弹幕被删除」通知，实际 err=%v", err)
	}
	if danmakuNotify.TargetID != danmakuID || danmakuNotify.VideoID != videoID {
		t.Fatalf("弹幕通知字段不符: %+v", danmakuNotify)
	}

	// -------- ③ 自己删自己的内容不提醒 --------
	selfCommentID := int64(8000000000000001006)
	if err := db.Create(&model.VideoComment{
		ID: selfCommentID, VideoID: videoID, UserID: authorID,
		Content: "自己删的", Status: model.CommentStatusNormal,
	}).Error; err != nil {
		t.Fatalf("准备自删评论失败: %v", err)
	}
	before := countNotifications(t, db, authorID, model.NotifyTypeCommentRemoved)
	if err := comments.AdminDelete(ctx, authorID, selfCommentID); err != nil {
		t.Fatalf("自删失败: %v", err)
	}
	after := countNotifications(t, db, authorID, model.NotifyTypeCommentRemoved)
	if after != before {
		t.Fatalf("作者自己删除不应产生通知，通知数 %d -> %d", before, after)
	}
	_ = db.Delete(&model.VideoComment{}, selfCommentID)
}

func countNotifications(t *testing.T, db *gorm.DB, userID int64, kind int8) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.Notification{}).
		Where("user_id = ? AND type = ?", userID, kind).Count(&n).Error; err != nil {
		t.Fatalf("统计通知失败: %v", err)
	}
	return n
}
