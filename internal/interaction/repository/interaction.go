// Package repository 负责互动域各关系表的数据访问。
//
// 物理拆库后本包只读写互动域自己的库（video_like / video_favorite /
// video_watch_later / video_watch_history / video_comment / video_danmaku /
// notification / video_stat），不再触碰视频库的任何表；需要视频信息时由
// service 层经 VideoProvider（gRPC）补全，需要校验视频存在性同理。
package repository

import (
	"context"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// Repository 封装互动域各表（点赞 / 收藏 / 稍后再看 / 观看历史 / 评论 / 弹幕 / 通知 / 计数）的数据库操作。
type Repository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// escapeLike 转义 LIKE 模式中的特殊字符（\ % _）。
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// 计数列名常量。作为 bumpStat 的入参使用，取值固定，不存在拼接注入。
const (
	columnLikeCount     = "like_count"
	columnFavoriteCount = "favorite_count"
)

// bumpStat 在事务内维护 video_stat 的计数：先幂等确保行存在，再按 delta 自增 / 自减。
//
// 下界用 GREATEST(..., 0) 保护，避免历史计数漂移把数值压成负数。
// column 只接受上面的包级常量。
func bumpStat(tx *gorm.DB, videoID int64, column string, delta int) error {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.VideoStat{VideoID: videoID}).
		Error; err != nil {
		return err
	}
	return tx.Model(&model.VideoStat{}).
		Where("video_id = ?", videoID).
		UpdateColumn(column, gorm.Expr("GREATEST("+column+" + ?, 0)", delta)).
		Error
}

// Like 点赞视频，返回点赞后的总数。
//
// 幂等：重复点赞不会重复计数。唯一键冲突时本次插入无行变更，也就跳过计数自增；
// 关系写入与计数自增放在同一事务内，避免两者不一致。
// 计数落在互动域自持的 video_stat 上，不再跨表写视频库。
func (r *Repository) Like(ctx context.Context, userID, videoID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.VideoLike{
			ID:      snowflake.NextID(),
			UserID:  userID,
			VideoID: videoID,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if err := bumpStat(tx, videoID, columnLikeCount, 1); err != nil {
				return err
			}
		}
		return tx.Model(&model.VideoStat{}).Where("video_id = ?", videoID).Pluck("like_count", &count).Error
	})
	return count, err
}

// Unlike 取消点赞，返回取消后的总数。
//
// 幂等：没有点赞记录时直接返回当前计数。计数做下界保护，
// 历史上若有计数漂移也不会出现负数。
func (r *Repository) Unlike(ctx context.Context, userID, videoID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("user_id = ? AND video_id = ?", userID, videoID).Delete(&model.VideoLike{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if err := bumpStat(tx, videoID, columnLikeCount, -1); err != nil {
				return err
			}
		}
		return tx.Model(&model.VideoStat{}).Where("video_id = ?", videoID).Pluck("like_count", &count).Error
	})
	return count, err
}

// IsLiked 判断用户是否已点赞该视频。
func (r *Repository) IsLiked(ctx context.Context, userID, videoID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.VideoLike{}).
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Count(&count).
		Error
	return count > 0, err
}

// Favorite 收藏视频，返回收藏后的总数。
//
// 幂等：重复收藏不会重复计数。唯一键冲突时本次插入无行变更，
// 也就跳过计数自增；计数与关系写入放在同一事务内，避免两者不一致。
func (r *Repository) Favorite(ctx context.Context, userID, videoID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.VideoFavorite{
			ID:      snowflake.NextID(),
			UserID:  userID,
			VideoID: videoID,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if err := bumpStat(tx, videoID, columnFavoriteCount, 1); err != nil {
				return err
			}
		}
		return tx.Model(&model.VideoStat{}).Where("video_id = ?", videoID).Pluck("favorite_count", &count).Error
	})
	return count, err
}

// Unfavorite 取消收藏，返回取消后的总数。
//
// 幂等：没有收藏记录时直接返回当前计数。计数做下界保护，
// 历史上若有计数漂移也不会出现负数。
func (r *Repository) Unfavorite(ctx context.Context, userID, videoID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("user_id = ? AND video_id = ?", userID, videoID).Delete(&model.VideoFavorite{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if err := bumpStat(tx, videoID, columnFavoriteCount, -1); err != nil {
				return err
			}
		}
		return tx.Model(&model.VideoStat{}).Where("video_id = ?", videoID).Pluck("favorite_count", &count).Error
	})
	return count, err
}

// IsFavorited 判断用户是否已收藏该视频。
func (r *Repository) IsFavorited(ctx context.Context, userID, videoID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.VideoFavorite{}).
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Count(&count).
		Error
	return count > 0, err
}

// GetStat 查询单个视频的计数快照；无记录时返回 gorm.ErrRecordNotFound。
//
// 供计数回写使用：互动侧改完计数后读一次全量快照发给视频域。
func (r *Repository) GetStat(ctx context.Context, videoID int64) (model.VideoStat, error) {
	var s model.VideoStat
	err := r.db.WithContext(ctx).Where("video_id = ?", videoID).Take(&s).Error
	return s, err
}

// StatCounts 批量查询视频计数，返回 video_id -> 计数 的映射。
//
// 用于「我」列表在组装视频卡片时补上点赞 / 收藏数（这些计数的真源在互动域）。
func (r *Repository) StatCounts(ctx context.Context, videoIDs []int64) (map[int64]model.VideoStat, error) {
	out := make(map[int64]model.VideoStat, len(videoIDs))
	if len(videoIDs) == 0 {
		return out, nil
	}
	var rows []model.VideoStat
	if err := r.db.WithContext(ctx).Where("video_id IN ?", videoIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, s := range rows {
		out[s.VideoID] = s
	}
	return out, nil
}

// WatchLater 加入稍后再看（幂等）。
//
// 与收藏不同，视频上没有「稍后再看」计数列，因此这里不做计数维护，
// 重复加入由唯一键兜住，无副作用。
func (r *Repository) WatchLater(ctx context.Context, userID, videoID int64) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.VideoWatchLater{
			ID:      snowflake.NextID(),
			UserID:  userID,
			VideoID: videoID,
		}).
		Error
}

// UnwatchLater 移出稍后再看（幂等）。
func (r *Repository) UnwatchLater(ctx context.Context, userID, videoID int64) error {
	return r.db.WithContext(ctx).
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Delete(&model.VideoWatchLater{}).
		Error
}

// IsWatchLater 判断用户是否已把该视频加入稍后再看。
func (r *Repository) IsWatchLater(ctx context.Context, userID, videoID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.VideoWatchLater{}).
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Count(&count).
		Error
	return count > 0, err
}

// ReportHistory 上报观看记录：已存在则更新进度与时间，不存在则插入。
func (r *Repository) ReportHistory(ctx context.Context, userID, videoID int64, progress int) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "video_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"progress", "updated_at"}),
		}).
		Create(&model.VideoWatchHistory{
			ID:       snowflake.NextID(),
			UserID:   userID,
			VideoID:  videoID,
			Progress: progress,
		}).
		Error
}

// GetHistoryProgress 查询用户对某视频的观看进度（秒），无记录时返回 0。
func (r *Repository) GetHistoryProgress(ctx context.Context, userID, videoID int64) (int, error) {
	var progress int
	err := r.db.WithContext(ctx).
		Model(&model.VideoWatchHistory{}).
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Pluck("progress", &progress).
		Error
	return progress, err
}

// RecentWatchedVideoIDs 取用户最近看过的 video_id，按最近观看时间倒序。
//
// video_watch_history 以 (user_id, video_id) 为唯一键，重复观看只覆盖 updated_at，
// 因此按 updated_at 倒序即最近观看顺序。limit <= 0 时返回空切片。
func (r *Repository) RecentWatchedVideoIDs(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		return []int64{}, nil
	}
	var ids []int64
	err := r.db.WithContext(ctx).
		Model(&model.VideoWatchHistory{}).
		Where("user_id = ?", userID).
		Order("updated_at DESC").
		Limit(limit).
		Pluck("video_id", &ids).
		Error
	return ids, err
}

// engagedRow 是点赞 / 收藏合并时携带创建时间的中间行。
type engagedRow struct {
	VideoID   int64     `gorm:"column:video_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// RecentEngagedVideoIDs 取用户最近点赞或收藏过的 video_id，去重后按时间倒序。
//
// 点赞与收藏分属两张表，两次查询各自只给出顺序、彼此的时间无法直接比较先后，
// 因此这里连同 created_at 一起取出，再在内存里合并排序；按 video_id 去重，
// 避免同一视频既点赞又收藏时重复出现。limit <= 0 时返回空切片。
func (r *Repository) RecentEngagedVideoIDs(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		return []int64{}, nil
	}

	var likes []engagedRow
	if err := r.db.WithContext(ctx).
		Model(&model.VideoLike{}).
		Select("video_id, created_at").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&likes).
		Error; err != nil {
		return nil, err
	}

	var favorites []engagedRow
	if err := r.db.WithContext(ctx).
		Model(&model.VideoFavorite{}).
		Select("video_id, created_at").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&favorites).
		Error; err != nil {
		return nil, err
	}

	rows := append(likes, favorites...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })

	seen := make(map[int64]struct{}, len(rows))
	out := make([]int64, 0, limit)
	for _, row := range rows {
		if _, ok := seen[row.VideoID]; ok {
			continue
		}
		seen[row.VideoID] = struct{}{}
		out = append(out, row.VideoID)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// ListLiked 分页查询用户点赞过的 video_id，按点赞时间倒序，返回该页 id 与总数。
//
// 拆库后不再 JOIN 视频表做「已上架」过滤与卡片字段填充：
// 这里只负责关系表的排序与分页，已上架过滤与卡片字段由 service 层经 VideoProvider
// 批量取回后处理（见 service.InteractionService.assembleCards）。
func (r *Repository) ListLiked(ctx context.Context, userID int64, offset, limit int) ([]int64, int64, error) {
	return r.listRelationVideoIDs(ctx, &model.VideoLike{}, "created_at", userID, offset, limit)
}

// ListFavorites 分页查询用户收藏的 video_id，按收藏时间倒序。
func (r *Repository) ListFavorites(ctx context.Context, userID int64, offset, limit int) ([]int64, int64, error) {
	return r.listRelationVideoIDs(ctx, &model.VideoFavorite{}, "created_at", userID, offset, limit)
}

// ListWatchLater 分页查询用户稍后再看的 video_id，按加入时间倒序。
func (r *Repository) ListWatchLater(ctx context.Context, userID int64, offset, limit int) ([]int64, int64, error) {
	return r.listRelationVideoIDs(ctx, &model.VideoWatchLater{}, "created_at", userID, offset, limit)
}

// ListHistory 分页查询用户观看历史里的 video_id，按最近观看时间倒序。
func (r *Repository) ListHistory(ctx context.Context, userID int64, offset, limit int) ([]int64, int64, error) {
	return r.listRelationVideoIDs(ctx, &model.VideoWatchHistory{}, "updated_at", userID, offset, limit)
}

// listRelationVideoIDs 是四个「我」列表共用的查询：按用户过滤、按时间倒序分页，返回 video_id 与总数。
//
// orderBy 由调用方传入固定列名（created_at / updated_at），非用户输入，无注入风险。
func (r *Repository) listRelationVideoIDs(ctx context.Context, table any, orderBy string, userID int64, offset, limit int) ([]int64, int64, error) {
	base := r.db.WithContext(ctx).Model(table).Where("user_id = ?", userID)

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var ids []int64
	if err := base.
		Order(orderBy+" DESC, id DESC").
		Offset(offset).Limit(limit).
		Pluck("video_id", &ids).
		Error; err != nil {
		return nil, 0, err
	}
	return ids, total, nil
}
