// Package service 提供首页推荐所需用户行为信号的只读查询服务。
package service

import (
	"context"
	"fmt"

	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
)

// defaultSignalLimit 是用户行为信号的默认条数（入参非法时兜底）。
//
// 首页只需要有限条历史用于排除与兴趣统计，给个上限既够用，
// 也能避免为个别用户拉出巨量行。
const defaultSignalLimit = 200

// SignalService 是首页推荐所需用户行为信号的薄查询服务。
type SignalService struct {
	repo *repository.Repository
}

// NewSignalService 构造 SignalService。
func NewSignalService(repo *repository.Repository) *SignalService {
	return &SignalService{repo: repo}
}

// GetUserSignals 返回首页推荐所需的用户行为信号：
// watched 是最近看过的 video_id（用于排除），engaged 是最近点赞 / 收藏过的 video_id（用于算兴趣分类）。
//
// userID 非法时直接返回空信号（首页对未登录宽容，不报错）；limit 非法时用默认值兜底。
// 两次仓储查询串行执行：彼此独立且都很快，串行比引入并发编排更简单。
func (s *SignalService) GetUserSignals(ctx context.Context, userID int64, limit int) (watched []int64, engaged []int64, err error) {
	if userID <= 0 {
		return []int64{}, []int64{}, nil
	}
	if limit <= 0 {
		limit = defaultSignalLimit
	}

	watched, err = s.repo.RecentWatchedVideoIDs(ctx, userID, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("查询最近观看记录失败: %w", err)
	}
	engaged, err = s.repo.RecentEngagedVideoIDs(ctx, userID, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("查询最近点赞收藏记录失败: %w", err)
	}
	return watched, engaged, nil
}
