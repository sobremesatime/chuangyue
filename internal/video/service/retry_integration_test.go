//go:build integration

// 转码重试的集成测试：需要本地 MySQL（口令从 MYSQL_PASSWORD 环境变量取），不可用则跳过。
//
//	go test -tags integration -v -run TestRetryTranscode ./internal/video/service/...
//
// 只覆盖「进不到投递」的几条路径：生产者指向不可达地址（kafka.Writer 惰性连接，
// 用到时才报错），因此不依赖 Kafka。
package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/mq"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/storage"
)

func TestRetryTranscodeGuards(t *testing.T) {
	db := newSlotTestDB(t)
	repo := videorepo.New(db)
	ctx := context.Background()

	// 生产者指向不可达地址：用于验证「投递失败要还原状态」这条分支。
	producer := mq.NewProducer([]string{"127.0.0.1:1"}, "chuangyue.transcode.task.it-retry")
	defer func() { _ = producer.Close() }()

	svc := New(repo, storage.NewLocal(t.TempDir()),
		config.UploadConfig{}, config.TranscodeConfig{MaxPending: 10}, producer, nil)

	create := func(status int8, sourceURL, errMsg string) *model.Video {
		t.Helper()
		v := &model.Video{
			ID:         snowflake.NextID(),
			Title:      "重转用例",
			UploaderID: 1,
			Status:     status,
			SourceURL:  sourceURL,
			ErrorMsg:   errMsg,
		}
		if err := repo.Create(ctx, v); err != nil {
			t.Fatalf("创建测试视频失败: %v", err)
		}
		t.Cleanup(func() { db.Delete(&model.Video{}, v.ID) })
		return v
	}

	// ① 视频不存在
	if _, err := svc.RetryTranscode(ctx, 1<<62); err == nil {
		t.Fatal("不存在的视频应报错")
	} else {
		var ec errcode.ErrCode
		if !errors.As(err, &ec) || ec.Code != errcode.ErrVideoNotFound.Code {
			t.Fatalf("期望 %d，实际 %v", errcode.ErrVideoNotFound.Code, err)
		}
	}

	// ② 只有「转码失败」可以重试
	published := create(model.StatusPublished, "/uploads/it-retry.mp4", "")
	if _, err := svc.RetryTranscode(ctx, published.ID); err == nil ||
		!strings.Contains(err.Error(), "只有转码失败的视频") {
		t.Fatalf("已上架视频不应允许重转，实际 err=%v", err)
	}

	// ③ 失败但缺源文件：无从重转，明确报错
	noSource := create(model.StatusFailed, "", "源文件缺失")
	if _, err := svc.RetryTranscode(ctx, noSource.ID); err == nil ||
		!strings.Contains(err.Error(), "源文件地址缺失") {
		t.Fatalf("缺源文件应被拒绝，实际 err=%v", err)
	}

	// ④ 投递失败（Kafka 不可达）：返回「服务繁忙」，并且**状态与失败原因都要还原**，
	//    否则视频会永久停在「转码中」，连重试入口都没有了。
	failed := create(model.StatusFailed, "/uploads/it-retry.mp4", "上次失败原因")
	_, err := svc.RetryTranscode(ctx, failed.ID)
	if err == nil {
		t.Fatal("投递失败应报错")
	}
	var ec errcode.ErrCode
	if !errors.As(err, &ec) || ec.Code != errcode.ErrServerBusy.Code {
		t.Fatalf("期望 %d，实际 err=%v", errcode.ErrServerBusy.Code, err)
	}

	got, err := repo.GetByID(ctx, failed.ID)
	if err != nil {
		t.Fatalf("回查视频失败: %v", err)
	}
	if got.Status != model.StatusFailed {
		t.Fatalf("投递失败后状态应还原为失败，实际 %d", got.Status)
	}
	if got.ErrorMsg != "上次失败原因" {
		t.Fatalf("投递失败后原失败原因应保留，实际 %q", got.ErrorMsg)
	}
}
