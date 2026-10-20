package service

import (
	"context"
	"errors"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// StatPublisher 把互动计数的全量快照投递给视频域（经消息队列异步回写只读副本）。
//
// 拆库后视频库的 like_count / favorite_count 只是供热度排序的副本，本端口负责通知它更新；
// 投递是 best-effort：失败只记日志，不影响点赞 / 收藏本身，副本在下一次变更时自会追平。
type StatPublisher interface {
	PublishVideoStat(ctx context.Context, stat videodto.VideoStatChanged) error
}

// InteractionService 承载用户与视频的互动：点赞、收藏、观看历史。
//
// 与视频服务关注点不同，单独成型，避免把「内容管理」与「用户互动」两类职责堆进同一个结构体。
type InteractionService struct {
	repo *repository.Repository
	// videos 是互动域取视频信息的端口，替代原先仓储对视频表的直读（存在性校验 + 卡片字段）
	videos VideoProvider
	// publisher 把计数变更异步回写给视频域，可为 nil（不回写）
	publisher StatPublisher
}

// NewInteractionService 构造 InteractionService。
func NewInteractionService(repo *repository.Repository, videos VideoProvider, publisher StatPublisher) *InteractionService {
	return &InteractionService{repo: repo, videos: videos, publisher: publisher}
}

// ensureVideo 校验视频是否存在。
//
// 已下架的视频仍允许互动，以保留用户的点赞 / 收藏 / 观看记录，
// 只是「我」分组下的四个列表不再展示已下架内容。拆库后存在性经视频服务判定。
func (s *InteractionService) ensureVideo(ctx context.Context, videoID int64) error {
	return ensureVideoExists(ctx, s.videos, videoID)
}

// Like 点赞视频。
func (s *InteractionService) Like(ctx context.Context, userID, videoID int64) (*dto.LikeResult, error) {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}
	count, err := s.repo.Like(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	s.publishStat(ctx, videoID)
	return &dto.LikeResult{Liked: true, LikeCount: count}, nil
}

// Unlike 取消点赞。
func (s *InteractionService) Unlike(ctx context.Context, userID, videoID int64) (*dto.LikeResult, error) {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}
	count, err := s.repo.Unlike(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	s.publishStat(ctx, videoID)
	return &dto.LikeResult{Liked: false, LikeCount: count}, nil
}

// Favorite 收藏视频。
func (s *InteractionService) Favorite(ctx context.Context, userID, videoID int64) (*dto.FavoriteResult, error) {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}
	count, err := s.repo.Favorite(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	s.publishStat(ctx, videoID)
	return &dto.FavoriteResult{Favorited: true, FavoriteCount: count}, nil
}

// Unfavorite 取消收藏。
func (s *InteractionService) Unfavorite(ctx context.Context, userID, videoID int64) (*dto.FavoriteResult, error) {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}
	count, err := s.repo.Unfavorite(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	s.publishStat(ctx, videoID)
	return &dto.FavoriteResult{Favorited: false, FavoriteCount: count}, nil
}

// State 查询当前用户对该视频的互动状态。
func (s *InteractionService) State(ctx context.Context, userID, videoID int64) (*dto.InteractionState, error) {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}

	liked, err := s.repo.IsLiked(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	favorited, err := s.repo.IsFavorited(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	watchLater, err := s.repo.IsWatchLater(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	progress, err := s.repo.GetHistoryProgress(ctx, userID, videoID)
	if err != nil {
		return nil, err
	}
	return &dto.InteractionState{
		Liked:      liked,
		Favorited:  favorited,
		WatchLater: watchLater,
		Progress:   progress,
	}, nil
}

// WatchLater 把视频加入稍后再看（幂等）。
func (s *InteractionService) WatchLater(ctx context.Context, userID, videoID int64) (*dto.WatchLaterResult, error) {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}
	if err := s.repo.WatchLater(ctx, userID, videoID); err != nil {
		return nil, err
	}
	return &dto.WatchLaterResult{WatchLater: true}, nil
}

// UnwatchLater 把视频移出稍后再看（幂等）。
func (s *InteractionService) UnwatchLater(ctx context.Context, userID, videoID int64) (*dto.WatchLaterResult, error) {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}
	if err := s.repo.UnwatchLater(ctx, userID, videoID); err != nil {
		return nil, err
	}
	return &dto.WatchLaterResult{WatchLater: false}, nil
}

// ReportHistory 上报观看历史，同一视频只保留一条并更新进度。
func (s *InteractionService) ReportHistory(ctx context.Context, userID, videoID int64, progress int) error {
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return err
	}
	if progress < 0 {
		progress = 0
	}
	return s.repo.ReportHistory(ctx, userID, videoID, progress)
}

// ListLiked 分页查询用户点赞过的视频。
func (s *InteractionService) ListLiked(ctx context.Context, userID int64, page, pageSize int) ([]videodto.VideoInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(page, pageSize)
	ids, total, err := s.repo.ListLiked(ctx, userID, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	list, err := s.assembleCards(ctx, ids)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	return list, total, p, ps, nil
}

// ListFavorites 分页查询用户收藏的视频。
func (s *InteractionService) ListFavorites(ctx context.Context, userID int64, page, pageSize int) ([]videodto.VideoInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(page, pageSize)
	ids, total, err := s.repo.ListFavorites(ctx, userID, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	list, err := s.assembleCards(ctx, ids)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	return list, total, p, ps, nil
}

// ListHistory 分页查询用户的观看历史，按最近观看时间倒序。
func (s *InteractionService) ListHistory(ctx context.Context, userID int64, page, pageSize int) ([]videodto.VideoInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(page, pageSize)
	ids, total, err := s.repo.ListHistory(ctx, userID, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	list, err := s.assembleCards(ctx, ids)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	return list, total, p, ps, nil
}

// ListWatchLater 分页查询用户稍后再看的视频，按加入时间倒序。
func (s *InteractionService) ListWatchLater(ctx context.Context, userID int64, page, pageSize int) ([]videodto.VideoInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(page, pageSize)
	ids, total, err := s.repo.ListWatchLater(ctx, userID, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	list, err := s.assembleCards(ctx, ids)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	return list, total, p, ps, nil
}

// assembleCards 把一页 video_id 组装成对外视频卡片。
//
// 三步：先经视频服务批量取回完整视频信息（一次 RPC，避免 N+1），再用互动域的
// video_stat 覆盖点赞 / 收藏计数（计数真源在互动域，视频库那份只是异步副本），
// 最后按原顺序过滤掉已不存在的视频与未上架的视频。
//
// 说明：过滤发生在内存里，因此「本页条数」可能少于 pageSize；总数沿用关系表计数
// （含已下架记录）——这是拆库后不再能跨库 JOIN 过滤 status 的已知取舍。
func (s *InteractionService) assembleCards(ctx context.Context, ids []int64) ([]videodto.VideoInfo, error) {
	if len(ids) == 0 {
		return []videodto.VideoInfo{}, nil
	}
	infos, err := loadVideoMap(ctx, s.videos, ids)
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.StatCounts(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]videodto.VideoInfo, 0, len(ids))
	for _, id := range ids {
		info, ok := infos[id]
		if !ok {
			continue
		}
		if info.Status != videomodel.StatusPublished {
			continue
		}
		stat := counts[id]
		out = append(out, videodto.VideoInfo{
			ID:            info.ID,
			Title:         info.Title,
			Description:   info.Description,
			CoverURL:      info.CoverURL,
			PlayURL:       info.PlayURL,
			Duration:      info.Duration,
			CategoryID:    info.CategoryID,
			ViewCount:     info.ViewCount,
			LikeCount:     stat.LikeCount,
			FavoriteCount: stat.FavoriteCount,
			Status:        info.Status,
			ErrorMsg:      info.ErrorMsg,
			CreatedAt:     info.CreatedAt,
		})
	}
	return out, nil
}

// publishStat 把某视频当前的计数快照投递给视频域，best-effort：失败只记日志。
func (s *InteractionService) publishStat(ctx context.Context, videoID int64) {
	if s.publisher == nil {
		return
	}
	stat, err := s.repo.GetStat(ctx, videoID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return
	}
	if err != nil {
		logger.L().Warn("读取互动计数失败，跳过回写",
			logger.TraceField(ctx), zap.Int64("video_id", videoID), zap.Error(err))
		return
	}
	if err := s.publisher.PublishVideoStat(ctx, videodto.VideoStatChanged{
		VideoID:       stat.VideoID,
		LikeCount:     stat.LikeCount,
		FavoriteCount: stat.FavoriteCount,
	}); err != nil {
		logger.L().Warn("互动计数回写投递失败",
			logger.TraceField(ctx), zap.Int64("video_id", videoID), zap.Error(err))
	}
}
