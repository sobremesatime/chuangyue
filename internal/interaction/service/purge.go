package service

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// PurgeService 负责「视频被删除后清理互动域关联数据」。
//
// 为什么要做：评论 / 弹幕 / 点赞 / 收藏 / 稍后再看 / 历史 / 通知都以 video_id 挂在互动库，
// 视频域删掉视频后它们就成了孤儿行——读路径会按视频是否存在过滤掉，所以不会报错，
// 但会永久占库，且「我的收藏」这类统计会越来越不准。
type PurgeService struct {
	repo *repository.Repository
}

// NewPurgeService 构造 PurgeService。
func NewPurgeService(repo *repository.Repository) *PurgeService {
	return &PurgeService{repo: repo}
}

// PurgeVideo 清理一个视频的全部互动数据（幂等，见 repository.PurgeVideoData）。
func (s *PurgeService) PurgeVideo(ctx context.Context, videoID int64) error {
	if videoID <= 0 {
		return fmt.Errorf("非法的 video_id: %d", videoID)
	}
	return s.repo.PurgeVideoData(ctx, videoID)
}

// HandleVideoDeleted 是 Kafka 消息处理器：反序列化事件后清理。
//
// 两种失败刻意区别对待：
//   - **反序列化失败返回 nil**：这是毒消息，重试多少次都不会好。返回错误会让
//     mq.Consumer 永远不提交位移、把整个分区卡死；记日志后跳过才是对的。
//   - **清库失败返回 error**：多半是数据库抖动，交给 mq.Consumer 的
//     「不提交 + 退避重试」机制，重投时按 video_id 重删一遍即可（幂等）。
func (s *PurgeService) HandleVideoDeleted(ctx context.Context, value []byte) error {
	var event videodto.VideoDeleted
	if err := json.Unmarshal(value, &event); err != nil {
		logger.L().Error("解析视频删除事件失败，跳过该消息",
			zap.ByteString("raw", value), zap.Error(err))
		return nil
	}
	if err := s.PurgeVideo(ctx, event.VideoID); err != nil {
		return fmt.Errorf("清理视频 %d 的互动数据失败: %w", event.VideoID, err)
	}
	logger.L().Info("视频删除事件已处理，互动数据已清理", zap.Int64("video_id", event.VideoID))
	return nil
}
