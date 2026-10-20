// Package service 承载视频域对互动计数回写的消费逻辑。
package service

import (
	"context"
	"encoding/json"
	"fmt"

	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
)

// ApplyVideoStat 处理一条来自 video.stat 主题的互动计数快照，刷新视频上的计数副本。
//
// 作为 mq.Handler 直接注册到消费组：返回 error 表示位移不提交，消息会在重启 /
// 再均衡后重投；因为快照写是幂等的，重投没有副作用。
// 消息格式非法时直接返回 error（属于程序错误，重投也不会变好，交由运维关注）。
func (s *QueryService) ApplyVideoStat(ctx context.Context, value []byte) error {
	var msg videodto.VideoStatChanged
	if err := json.Unmarshal(value, &msg); err != nil {
		return fmt.Errorf("解析互动计数消息失败: %w", err)
	}
	if msg.VideoID <= 0 {
		return nil
	}
	// 计数不应为负，兜一下避免脏数据把热度排序带偏。
	if msg.LikeCount < 0 {
		msg.LikeCount = 0
	}
	if msg.FavoriteCount < 0 {
		msg.FavoriteCount = 0
	}
	return s.repo.UpdateInteractionCounts(ctx, msg.VideoID, msg.LikeCount, msg.FavoriteCount)
}
