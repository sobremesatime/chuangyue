// Package repository 负责互动计数的批量只读查询。
package repository

import (
	"context"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
)

// VideoStats 是单个视频的互动计数。
type VideoStats struct {
	VideoID       int64
	LikeCount     int64
	FavoriteCount int64
	CommentCount  int64
}

// VideoStats 批量查询一组视频的点赞、收藏与评论计数。
//
// 拆库后点赞 / 收藏的计数真源在互动域的 video_stat，不再读视频表；
// 对「视频是否存在」不再负责——存在性由调用方经视频服务判定。
// 入参为空返回空切片；对每个请求的 id 都返回一行（无记录则计数为 0），
// 由调用方按 id 归位。
func (r *Repository) VideoStats(ctx context.Context, videoIDs []int64) ([]VideoStats, error) {
	if len(videoIDs) == 0 {
		return []VideoStats{}, nil
	}

	// 第一次查询：点赞 / 收藏计数（来自互动域的 video_stat）
	var stats []model.VideoStat
	if err := r.db.WithContext(ctx).
		Where("video_id IN ?", videoIDs).
		Find(&stats).
		Error; err != nil {
		return nil, err
	}
	counts := make(map[int64]model.VideoStat, len(stats))
	for _, s := range stats {
		counts[s.VideoID] = s
	}

	// 第二次查询：正常评论按视频分组的条数
	type commentCounter struct {
		VideoID int64 `gorm:"column:video_id"`
		Count   int64 `gorm:"column:cnt"`
	}
	var commentCounters []commentCounter
	if err := r.db.WithContext(ctx).
		Model(&model.VideoComment{}).
		Select("video_id, COUNT(*) AS cnt").
		Where("video_id IN ?", videoIDs).
		Where("status = ?", model.CommentStatusNormal).
		Group("video_id").
		Find(&commentCounters).
		Error; err != nil {
		return nil, err
	}
	commentCounts := make(map[int64]int64, len(commentCounters))
	for _, c := range commentCounters {
		commentCounts[c.VideoID] = c.Count
	}

	out := make([]VideoStats, 0, len(videoIDs))
	for _, id := range videoIDs {
		s := counts[id]
		out = append(out, VideoStats{
			VideoID:       id,
			LikeCount:     s.LikeCount,
			FavoriteCount: s.FavoriteCount,
			CommentCount:  commentCounts[id],
		})
	}
	return out, nil
}
