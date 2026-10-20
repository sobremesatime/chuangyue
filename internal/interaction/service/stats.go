// Package service 提供互动计数的只读查询服务。
package service

import (
	"context"

	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
)

// StatsService 是互动计数的薄查询服务。
//
// 只做入参规整与委托，具体 SQL 都在仓储层，便于被 gRPC 与其它调用方复用。
type StatsService struct {
	repo *repository.Repository
}

// NewStatsService 构造 StatsService。
func NewStatsService(repo *repository.Repository) *StatsService {
	return &StatsService{repo: repo}
}

// GetVideoStats 批量查询视频互动计数。
//
// 入参先去重、剔除非法 id（<= 0），再交给仓储批量查询。
func (s *StatsService) GetVideoStats(ctx context.Context, ids []int64) ([]repository.VideoStats, error) {
	normalized := normalizeVideoIDs(ids)
	if len(normalized) == 0 {
		return []repository.VideoStats{}, nil
	}
	return s.repo.VideoStats(ctx, normalized)
}

// normalizeVideoIDs 去重并剔除非法 id，保持首次出现的顺序。
func normalizeVideoIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
