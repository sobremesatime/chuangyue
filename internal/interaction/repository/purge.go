package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
)

// PurgeVideoData 删除某个视频在互动域的全部关联数据。
//
// 场景：视频被管理员删除后，视频域投递 chuangyue.video.deleted，本方法负责清库。
//
// 三个要点：
//   - **幂等**：全部是 DELETE ... WHERE video_id = ?，重复消费只是把同一批行再删一遍。
//     这是必须的——mq.Consumer 是至少一次语义，消息可能被重复投递。
//   - **事务**：十条 DELETE 放在一个事务里，避免中途失败留下「评论删了、点赞还在」的半截状态。
//   - **顺序**：先删子表（评论点赞挂在 comment_id 上，没有 video_id），再删主表。
func (r *Repository) PurgeVideoData(ctx context.Context, videoID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 评论点赞：表里只有 comment_id，只能先定位该视频下的评论 id。
		commentIDs := tx.Model(&model.VideoComment{}).Select("id").Where("video_id = ?", videoID)
		if err := tx.Where("comment_id IN (?)", commentIDs).Delete(&model.VideoCommentLike{}).Error; err != nil {
			return err
		}

		// 其余表都以 video_id 为条件；顺序无关，统一按「子 → 父 → 计数 → 通知」列出，
		// 方便以后新增关联表时照抄一行。
		for _, target := range []any{
			&model.VideoComment{},
			&model.VideoDanmaku{},
			&model.VideoLike{},
			&model.VideoFavorite{},
			&model.VideoWatchLater{},
			&model.VideoWatchHistory{},
			&model.VideoStat{},
			&model.Notification{},
		} {
			if err := tx.Where("video_id = ?", videoID).Delete(target).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
