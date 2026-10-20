//go:build integration

// 视频索引事件发布的集成测试：需要本地 MySQL（口令从 MYSQL_PASSWORD 环境变量取），不可用则跳过。
//
//	go test -tags integration -v -run TestIndexEventsOnVideoChanges ./internal/video/service/...
//
// 用真实的视频写入路径（上下架 / 编辑 / 转码回写 / 删除）驱动，断言发出的索引事件；
// 不依赖 Kafka 与 ES——发布者换成内存记录器，验证的是「哪些变更会发、发的是 upsert 还是 delete」。
package service

import (
	"context"
	"testing"
	"time"

	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/storage"
)

// recordingIndexPublisher 记录发出的索引事件，供断言。
type recordingIndexPublisher struct {
	events []dto.VideoIndexEvent
}

func (p *recordingIndexPublisher) PublishVideoIndex(_ context.Context, event dto.VideoIndexEvent) error {
	p.events = append(p.events, event)
	return nil
}

// last 返回最近一条事件；没有事件时直接失败。
func (p *recordingIndexPublisher) last(t *testing.T) dto.VideoIndexEvent {
	t.Helper()
	if len(p.events) == 0 {
		t.Fatal("期望发出索引事件，实际一条都没有")
	}
	return p.events[len(p.events)-1]
}

func TestIndexEventsOnVideoChanges(t *testing.T) {
	db := newSlotTestDB(t)
	repo := videorepo.New(db)
	pub := &recordingIndexPublisher{}
	ctx := context.Background()

	// producer 传 nil：本用例只走不依赖转码任务投递的路径。
	svc := New(repo, storage.NewLocal(t.TempDir()),
		config.UploadConfig{}, config.TranscodeConfig{}, nil, nil,
		WithIndexPublisher(pub))

	v := &model.Video{
		ID:          snowflake.NextID(),
		Title:       "索引同步用例",
		Description: "索引同步简介",
		CategoryID:  7,
		UploaderID:  1,
		Status:      model.StatusPublished,
		SourceURL:   "/uploads/it-index.mp4",
		CreatedAt:   time.Now(),
	}
	if err := repo.Create(ctx, v); err != nil {
		t.Fatalf("创建测试视频失败: %v", err)
	}
	t.Cleanup(func() { db.Where("id = ?", v.ID).Delete(&model.Video{}) })

	// ① 下架：从索引移除，且事件里带 video_id。
	if _, err := svc.UpdateStatus(ctx, v.ID, model.StatusOffline); err != nil {
		t.Fatalf("下架失败: %v", err)
	}
	evt := pub.last(t)
	if evt.Action != dto.VideoIndexDelete || evt.VideoID != v.ID {
		t.Fatalf("下架应发出 delete 事件, 实际 %+v", evt)
	}

	// ② 重新上架：覆盖写索引，文档带上检索所需字段。
	if _, err := svc.UpdateStatus(ctx, v.ID, model.StatusPublished); err != nil {
		t.Fatalf("上架失败: %v", err)
	}
	evt = pub.last(t)
	if evt.Action != dto.VideoIndexUpsert || evt.Video == nil {
		t.Fatalf("上架应发出 upsert 事件, 实际 %+v", evt)
	}
	if evt.Video.ID != v.ID || evt.Video.Title != "索引同步用例" ||
		evt.Video.CategoryID != 7 || evt.Video.CreatedAt.IsZero() {
		t.Fatalf("索引文档字段不完整: %+v", evt.Video)
	}

	// ③ 编辑标题：已上架视频必须重新写索引，否则搜索结果还是旧标题。
	newTitle := "索引同步用例-改"
	if _, err := svc.Update(ctx, v.ID, dto.UpdateVideoInput{Title: &newTitle}); err != nil {
		t.Fatalf("编辑视频失败: %v", err)
	}
	evt = pub.last(t)
	if evt.Action != dto.VideoIndexUpsert || evt.Video == nil || evt.Video.Title != newTitle {
		t.Fatalf("编辑后应发出带新标题的 upsert 事件, 实际 %+v", evt)
	}

	// ④ 转码回写失败：从索引移除（失败视频不该被检索到）。
	if err := svc.ReportTranscodeResult(ctx, v.ID, "", 0, model.StatusFailed, "转码失败"); err != nil {
		t.Fatalf("回写转码失败结果出错: %v", err)
	}
	evt = pub.last(t)
	if evt.Action != dto.VideoIndexDelete || evt.VideoID != v.ID {
		t.Fatalf("转码失败应发出 delete 事件, 实际 %+v", evt)
	}

	// ⑤ 转码成功：重新写索引（这是新视频进入搜索结果的唯一入口）。
	if err := svc.ReportTranscodeResult(ctx, v.ID, "/uploads/hls/x.m3u8", 120, model.StatusPublished, ""); err != nil {
		t.Fatalf("回写转码成功结果出错: %v", err)
	}
	evt = pub.last(t)
	if evt.Action != dto.VideoIndexUpsert || evt.Video == nil || evt.Video.ID != v.ID {
		t.Fatalf("转码成功应发出 upsert 事件, 实际 %+v", evt)
	}

	// ⑥ 删除：从索引移除。
	if err := svc.Delete(ctx, v.ID); err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}
	evt = pub.last(t)
	if evt.Action != dto.VideoIndexDelete || evt.VideoID != v.ID {
		t.Fatalf("删除应发出 delete 事件, 实际 %+v", evt)
	}
}
