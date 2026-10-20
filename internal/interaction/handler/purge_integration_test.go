//go:build integration

// 视频删除后清理互动数据的集成测试：需要本地 MySQL。
//
// 钉住三件事：
//   - 只删目标视频的数据，别的视频一行不动；
//   - 评论点赞（只有 comment_id）也要被正确清掉；
//   - 重复执行幂等（Kafka 至少一次投递会重放消息），毒消息直接跳过而不是卡住分区。
//
// 依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v -run TestPurgeVideoData ./internal/interaction/handler/...
package handler

import (
	"context"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
	interactionrepo "github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/database"
)

// newInteractionTestDB 连互动域自己的库（chuangyue_interaction），连不上直接跳过。
//
// 不复用 newTestDB：那个连的是主库 chuangyue。互动服务实际写的是物理拆出的库，
// 本用例要验的正是它。
func newInteractionTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	cfg := config.MySQLConfig{
		Host:         envOr("MYSQL_HOST", "127.0.0.1"),
		Port:         envIntOr("MYSQL_PORT", 3306),
		Username:     envOr("MYSQL_USER", "root"),
		Password:     envOr("MYSQL_PASSWORD", "root123456"),
		Database:     envOr("INTERACTION_MYSQL_DATABASE", "chuangyue_interaction"),
		Charset:      "utf8mb4",
		MaxIdleConns: 2,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	}
	db, err := database.NewMySQL(cfg)
	if err != nil {
		t.Skipf("互动库不可用，跳过集成测试: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

func TestPurgeVideoData(t *testing.T) {
	db := newInteractionTestDB(t)
	repo := interactionrepo.New(db)
	ctx := context.Background()

	// 用远离雪花区间的固定 ID：本用例只关心「按 video_id 精确清理」，
	// 固定值比随机值更容易定位残留数据。
	const target, other = int64(8000000000000000001), int64(8000000000000000002)

	cleanup := func() {
		for _, id := range []int64{target, other} {
			_ = repo.PurgeVideoData(context.Background(), id)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// seed 给一个视频铺满所有互动表，返回它的评论 id（评论点赞要靠它定位）。
	seed := func(videoID int64) int64 {
		t.Helper()
		comment := &model.VideoComment{
			ID:        videoID + 10,
			VideoID:   videoID,
			UserID:    1,
			Content:   "集成测试评论",
			Status:    model.CommentStatusNormal,
			LikeCount: 1,
		}
		mustExec(t, db.Create(comment).Error)
		mustExec(t, db.Create(&model.VideoCommentLike{
			ID: videoID + 11, CommentID: comment.ID, UserID: 2,
		}).Error)
		mustExec(t, db.Create(&model.VideoDanmaku{
			ID: videoID + 12, VideoID: videoID, UserID: 1, Content: "弹幕",
			TimeMs: 1000, Color: "#FFFFFF", Mode: model.DanmakuModeScroll, Status: model.DanmakuStatusNormal,
		}).Error)
		mustExec(t, db.Create(&model.VideoLike{ID: videoID + 13, UserID: 1, VideoID: videoID}).Error)
		mustExec(t, db.Create(&model.VideoFavorite{ID: videoID + 14, UserID: 1, VideoID: videoID}).Error)
		mustExec(t, db.Create(&model.VideoWatchLater{ID: videoID + 15, UserID: 1, VideoID: videoID}).Error)
		mustExec(t, db.Create(&model.VideoWatchHistory{
			ID: videoID + 16, UserID: 1, VideoID: videoID, Progress: 42,
		}).Error)
		mustExec(t, db.Create(&model.VideoStat{
			VideoID: videoID, LikeCount: 1, FavoriteCount: 1,
		}).Error)
		mustExec(t, db.Create(&model.Notification{
			ID: videoID + 17, UserID: 1, ActorID: 2, Type: model.NotifyTypeVideoCommented,
			TargetID: comment.ID, VideoID: videoID, Content: "收到评论",
		}).Error)
		return comment.ID
	}

	targetCommentID := seed(target)
	otherCommentID := seed(other)

	// -------- ① 清理目标视频：九张表全部归零 --------
	if err := repo.PurgeVideoData(ctx, target); err != nil {
		t.Fatalf("PurgeVideoData 失败: %v", err)
	}

	for _, check := range []struct {
		name  string
		model any
	}{
		{"video_comment", &model.VideoComment{}},
		{"video_danmaku", &model.VideoDanmaku{}},
		{"video_like", &model.VideoLike{}},
		{"video_favorite", &model.VideoFavorite{}},
		{"video_watch_later", &model.VideoWatchLater{}},
		{"video_watch_history", &model.VideoWatchHistory{}},
		{"video_stat", &model.VideoStat{}},
		{"notification", &model.Notification{}},
	} {
		var count int64
		if err := db.Model(check.model).Where("video_id = ?", target).Count(&count).Error; err != nil {
			t.Fatalf("统计 %s 失败: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s 应被清空，实际还剩 %d 行", check.name, count)
		}
	}

	// 评论点赞没有 video_id，只能按评论 id 校验——这正是 purge 里那条子查询要挡住的点。
	var likeCount int64
	if err := db.Model(&model.VideoCommentLike{}).Where("comment_id = ?", targetCommentID).
		Count(&likeCount).Error; err != nil {
		t.Fatalf("统计评论点赞失败: %v", err)
	}
	if likeCount != 0 {
		t.Fatalf("video_comment_like 应随评论一并清空，实际还剩 %d 行", likeCount)
	}

	// -------- ② 别的视频一行不动 --------
	var otherComments int64
	if err := db.Model(&model.VideoComment{}).Where("video_id = ?", other).Count(&otherComments).Error; err != nil {
		t.Fatalf("统计另一视频的评论失败: %v", err)
	}
	if otherComments != 1 {
		t.Fatalf("另一视频的评论不应受影响，期望 1 行，实际 %d 行", otherComments)
	}
	var otherLikes int64
	if err := db.Model(&model.VideoCommentLike{}).Where("comment_id = ?", otherCommentID).
		Count(&otherLikes).Error; err != nil {
		t.Fatalf("统计另一视频的评论点赞失败: %v", err)
	}
	if otherLikes != 1 {
		t.Fatalf("另一视频的评论点赞不应受影响，期望 1 行，实际 %d 行", otherLikes)
	}

	// -------- ③ 幂等：重复执行不报错（Kafka 至少一次会重放）--------
	if err := repo.PurgeVideoData(ctx, target); err != nil {
		t.Fatalf("重复清理应幂等，实际 err=%v", err)
	}

	// -------- ④ 消息处理器：毒消息跳过、合法消息清理 --------
	purgeSvc := service.NewPurgeService(repo)
	if err := purgeSvc.HandleVideoDeleted(ctx, []byte("not-a-json")); err != nil {
		t.Fatalf("毒消息应被跳过（返回 nil），实际 err=%v", err)
	}
	if err := purgeSvc.HandleVideoDeleted(ctx, []byte(fmt.Sprintf(`{"video_id":%d}`, other))); err != nil {
		t.Fatalf("处理删除事件失败: %v", err)
	}
	var remaining int64
	if err := db.Model(&model.VideoComment{}).Where("video_id = ?", other).Count(&remaining).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("事件处理器应清空另一视频的评论，实际还剩 %d 行", remaining)
	}

	// -------- ⑤ 非法 id 直接拒绝（防止误传 0 把全表清空）--------
	if err := purgeSvc.PurgeVideo(ctx, 0); err == nil {
		t.Fatal("video_id=0 应被拒绝")
	}
}

// mustExec 是测试内的小工具：SQL 失败立刻终止用例。
func mustExec(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("准备测试数据失败: %v", err)
	}
}
