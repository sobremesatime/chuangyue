//go:build integration

// 视频服务「转码状态读写」两个内部 RPC 的集成测试：需要本地 MySQL 与 Etcd。
//
// 覆盖 GetTranscodeState（存在 / 不存在）与 ReportTranscodeResult（写回后查库断言）；
// 临时行用 t.Cleanup 精确清理（只删自己插入的那一行）。依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/video/handler/...
package handler

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	videorepo "github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/internal/video/service"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/registry"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// setupTranscodeGRPCClient 起一个视频 gRPC 服务并注册到 Etcd，返回经 etcd 发现的客户端与 db。
func setupTranscodeGRPCClient(t *testing.T) (videov1.VideoServiceClient, *gorm.DB) {
	t.Helper()

	db := newTestDB(t)
	reg := newTestRegistry(t)
	registry.RegisterResolver(reg)

	srv := grpc.NewServer()
	videov1.RegisterVideoServiceServer(srv, NewGRPCVideoServer(service.New(videorepo.New(db), nil, config.UploadConfig{}, config.TranscodeConfig{}, nil, nil)))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// 用真实监听端口注册，服务名带随机后缀保证互不干扰
	serviceName := uniqueTestService("it-video-tc")
	deregister, err := reg.Register(context.Background(), serviceName, lis.Addr().String())
	if err != nil {
		t.Fatalf("注册服务实例失败: %v", err)
	}
	t.Cleanup(deregister)

	conn, err := grpc.NewClient("etcd:///"+serviceName,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return videov1.NewVideoServiceClient(conn), db
}

// TestGRPCTranscodeRPCs 覆盖 GetTranscodeState 与 ReportTranscodeResult 两个内部 RPC。
func TestGRPCTranscodeRPCs(t *testing.T) {
	client, db := setupTranscodeGRPCClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// GetTranscodeState：不存在的视频必须是 NotFound
	absentID := int64(1 << 62)
	if _, err := client.GetTranscodeState(ctx, &videov1.GetTranscodeStateRequest{VideoId: absentID}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetTranscodeState(不存在) 应返回 NotFound, 实际: %v", err)
	}

	// 造一条「转码中」的临时视频，source_url 指向不存在的文件（本用例只验状态读写，不真正转码）
	videoID := snowflake.NextID()
	const sourceURL = "/uploads/it-transcode-not-exist.mp4"
	if err := db.Create(&videomodel.Video{
		ID:        videoID,
		Title:     "it-transcode-video",
		SourceURL: sourceURL,
		Status:    videomodel.StatusTranscoding,
	}).Error; err != nil {
		t.Fatalf("插入临时视频失败: %v", err)
	}
	t.Cleanup(func() { db.Delete(&videomodel.Video{}, videoID) })

	// GetTranscodeState：存在的视频返回 id / status / source_url
	state, err := client.GetTranscodeState(ctx, &videov1.GetTranscodeStateRequest{VideoId: videoID})
	if err != nil {
		t.Fatalf("GetTranscodeState(%d) 失败: %v", videoID, err)
	}
	if state.GetVideoId() != videoID || int8(state.GetStatus()) != videomodel.StatusTranscoding {
		t.Fatalf("GetTranscodeState(%d) 返回 id=%d status=%d", videoID, state.GetVideoId(), state.GetStatus())
	}
	if state.GetSourceUrl() != sourceURL {
		t.Fatalf("GetTranscodeState(%d) 的 source_url 应为 %q，实际 %q", videoID, sourceURL, state.GetSourceUrl())
	}

	// ReportTranscodeResult：写回成功结果后查库断言
	const playURL = "/uploads/hls/it/master.m3u8"
	if _, err := client.ReportTranscodeResult(ctx, &videov1.ReportTranscodeResultRequest{
		VideoId:  videoID,
		PlayUrl:  playURL,
		Duration: 42,
		Status:   int32(videomodel.StatusPublished),
	}); err != nil {
		t.Fatalf("ReportTranscodeResult(成功) 失败: %v", err)
	}
	assertTranscodeRow(t, db, videoID, videomodel.StatusPublished, playURL, 42, "")

	// ReportTranscodeResult：写回失败结果后查库断言 error_msg 落库、play_url 被清空
	const errMsg = "ffmpeg 失败：源文件损坏"
	if _, err := client.ReportTranscodeResult(ctx, &videov1.ReportTranscodeResultRequest{
		VideoId:  videoID,
		Status:   int32(videomodel.StatusFailed),
		ErrorMsg: errMsg,
	}); err != nil {
		t.Fatalf("ReportTranscodeResult(失败) 失败: %v", err)
	}
	assertTranscodeRow(t, db, videoID, videomodel.StatusFailed, "", 0, errMsg)
}

// assertTranscodeRow 读回临时行，断言转码结果列已被写入。
func assertTranscodeRow(t *testing.T, db *gorm.DB, videoID int64, status int8, playURL string, duration int, errMsg string) {
	t.Helper()

	var got videomodel.Video
	if err := db.Where("id = ?", videoID).First(&got).Error; err != nil {
		t.Fatalf("读回视频 %d 失败: %v", videoID, err)
	}
	if got.Status != status {
		t.Fatalf("status 应为 %d，实际 %d", status, got.Status)
	}
	if got.PlayURL != playURL {
		t.Fatalf("play_url 应为 %q，实际 %q", playURL, got.PlayURL)
	}
	if got.Duration != duration {
		t.Fatalf("duration 应为 %d，实际 %d", duration, got.Duration)
	}
	if got.ErrorMsg != errMsg {
		t.Fatalf("error_msg 应为 %q，实际 %q", errMsg, got.ErrorMsg)
	}
}
