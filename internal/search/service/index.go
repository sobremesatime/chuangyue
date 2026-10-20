// Package service 承载检索索引的写入侧逻辑：消费视频变更事件、维护索引文档。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// IndexWriter 是索引的写入能力（由 pkg/es 实现）。
type IndexWriter interface {
	Index(ctx context.Context, id string, doc any) error
	Delete(ctx context.Context, id string) error
}

// IndexStore 是回填所需的最小能力：先数文档数，再逐条写入。
type IndexStore interface {
	IndexWriter
	Count(ctx context.Context) (int64, error)
}

// IndexService 把视频索引事件应用到检索索引。
type IndexService struct {
	writer IndexWriter
}

// NewIndexService 构造 IndexService。
func NewIndexService(writer IndexWriter) *IndexService {
	return &IndexService{writer: writer}
}

// HandleVideoIndex 处理一条来自 chuangyue.video.index 的消息。
//
// 作为 mq.Handler 注册到消费组：返回 error 表示位移不提交，Kafka 会在重启 / 再均衡后
// 重投；覆盖写与删除都是幂等的，重投没有副作用。
//
// 消息格式非法时只记日志并返回 nil：这类消息重投一万次也不会变好，
// 让它卡住分区反而会阻塞后续正常事件（与互动域清理消费者的取舍一致）。
func (s *IndexService) HandleVideoIndex(ctx context.Context, value []byte) error {
	var event videodto.VideoIndexEvent
	if err := json.Unmarshal(value, &event); err != nil {
		logger.L().Error("解析视频索引事件失败，跳过该消息", logger.TraceField(ctx), zap.Error(err))
		return nil
	}

	switch event.Action {
	case videodto.VideoIndexUpsert:
		if event.Video == nil || event.Video.ID <= 0 {
			logger.L().Warn("视频索引事件缺少文档内容，跳过", logger.TraceField(ctx))
			return nil
		}
		return s.writer.Index(ctx, strconv.FormatInt(event.Video.ID, 10), event.Video)

	case videodto.VideoIndexDelete:
		id := event.VideoID
		if id <= 0 && event.Video != nil {
			id = event.Video.ID
		}
		if id <= 0 {
			logger.L().Warn("视频索引删除事件缺少 video_id，跳过", logger.TraceField(ctx))
			return nil
		}
		return s.writer.Delete(ctx, strconv.FormatInt(id, 10))

	default:
		logger.L().Warn("未知的视频索引事件动作，跳过",
			logger.TraceField(ctx), zap.String("action", event.Action))
		return nil
	}
}

// 回填的分页参数与上限。
//
// 上限是兜底：回填只发生在「索引为空」时，若视频服务返回的 total 异常（例如分页
// 逻辑有 bug 导致永远取不完），不能让它无限循环下去。当前平台规模远低于这个数。
const (
	backfillPageSize = 100
	backfillMaxDocs  = 10000
)

// ReindexIfEmpty 在索引为空时从视频服务全量回填已上架视频。
//
// 为什么需要它：索引是派生数据，首次部署时 Kafka 里的历史事件并不存在，
// 只靠增量事件会让搜索一直是空的；索引被误删 / 重建后同理。
// 只在 count==0 时执行，避免每次重启都把整张视频表重新写一遍。
//
// 调用方应在后台 goroutine 中执行：视频服务可能比搜索服务晚就绪，
// 失败时由调用方退避重试，而不是阻塞搜索服务启动。
func ReindexIfEmpty(ctx context.Context, video videov1.VideoServiceClient, store IndexStore) error {
	count, err := store.Count(ctx)
	if err != nil {
		return fmt.Errorf("统计检索索引文档数失败: %w", err)
	}
	if count > 0 {
		return nil
	}

	indexed := 0
	for page := 1; ; page++ {
		resp, err := video.ListVideos(ctx, &videov1.ListVideosRequest{
			Page:     int32(page),
			PageSize: backfillPageSize,
		})
		if err != nil {
			return fmt.Errorf("回填检索索引时查询视频失败: %w", err)
		}

		videos := resp.GetVideos()
		for _, v := range videos {
			doc := videodto.VideoIndexDoc{
				ID:          v.GetId(),
				Title:       v.GetTitle(),
				Description: v.GetDescription(),
				CategoryID:  v.GetCategoryId(),
				CreatedAt:   protoTime(v.GetCreatedAt()),
			}
			if err := store.Index(ctx, strconv.FormatInt(doc.ID, 10), doc); err != nil {
				return fmt.Errorf("回填视频 %d 到检索索引失败: %w", doc.ID, err)
			}
			indexed++
			if indexed >= backfillMaxDocs {
				logger.L().Warn("检索索引回填达到上限，剩余视频将在后续变更时补齐",
					zap.Int("indexed", indexed))
				return nil
			}
		}

		if len(videos) < backfillPageSize || int64(indexed) >= resp.GetTotal() {
			break
		}
	}

	logger.L().Info("检索索引回填完成", zap.Int("indexed", indexed))
	return nil
}

// protoTime 把 protobuf 时间戳转成 time.Time；空值回落到零值，
// 避免一条缺字段的老数据让整次回填失败。
func protoTime(ts *timestamppb.Timestamp) (out time.Time) {
	if ts == nil {
		return
	}
	return ts.AsTime()
}
