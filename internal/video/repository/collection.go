package repository

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// ErrCollectionOrderMismatch 表示重排入参与库内视频集合不一致。
//
// 放在仓储层是因为「集合是否一致」必须在同一个事务里、与写 sort 一起判断：
// 先查后写在并发下会形成检查-使用竞态（另一个请求刚加/删视频，重排就会漏掉或写错行）。
var ErrCollectionOrderMismatch = errors.New("合集内的视频集合与传入顺序不一致")

// CreateCollection 插入一条合集记录。
func (r *Repository) CreateCollection(ctx context.Context, c *model.Collection) error {
	return r.db.WithContext(ctx).Create(c).Error
}

// GetCollectionByID 按主键查询合集，未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetCollectionByID(ctx context.Context, id int64) (*model.Collection, error) {
	var c model.Collection
	if err := r.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCollections 分页查询合集，按创建时间倒序。
//
// onlyPublic 为真时只返回 status=1（公开）的合集，供前台列表；为假时含草稿，供管理端。
// 用参数区分而不是拆成两个方法：两者的查询形状完全相同，唯一的差别就是这个过滤条件。
func (r *Repository) ListCollections(ctx context.Context, onlyPublic bool, offset, limit int) ([]model.Collection, int64, error) {
	query := r.db.WithContext(ctx).Model(&model.Collection{})
	if onlyPublic {
		query = query.Where("status = ?", model.CollectionStatusPublic)
	}

	var total int64
	// 用 Session 隔离，避免 Count 产生的临时子句污染后续 Find。
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []model.Collection
	if err := query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// UpdateCollectionFields 按列部分更新合集。
//
// 用 map 而不是结构体：GORM 的结构体更新会跳过零值字段，
// 那样「把封面清空成空串」「把状态改成 0（草稿）」都会被静默丢弃。
// fields 为空时直接返回，不产生任何写操作。
func (r *Repository) UpdateCollectionFields(ctx context.Context, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Model(&model.Collection{}).
		Where("id = ?", id).
		Updates(fields).
		Error
}

// DeleteCollection 删除合集及其全部关联行。
//
// 关联行必须显式删除：没有外键，留着只会成为孤儿数据。
// 合集封面是外部地址（本域不负责上传），因此不涉及文件清理。
func (r *Repository) DeleteCollection(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("collection_id = ?", id).Delete(&model.CollectionVideo{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Collection{}, id).Error
	})
}

// ListCollectionVideos 返回合集内的关联行，按 (sort, id) 升序。
//
// sort 允许重复（并发追加可能撞号），用 id 兜底保证顺序稳定且可复现。
func (r *Repository) ListCollectionVideos(ctx context.Context, collectionID int64) ([]model.CollectionVideo, error) {
	var list []model.CollectionVideo
	err := r.db.WithContext(ctx).
		Where("collection_id = ?", collectionID).
		Order("sort ASC, id ASC").
		Find(&list).
		Error
	return list, err
}

// ListCollectionVideosByCollections 批量返回多个合集的关联行。
//
// 按 (collection_id, sort, id) 升序：同一个合集的关联行在结果里连续且有序，
// 调用方一次扫描就能同时得到「每个合集的播放顺序」，不必按合集逐个查询。
func (r *Repository) ListCollectionVideosByCollections(ctx context.Context, collectionIDs []int64) ([]model.CollectionVideo, error) {
	if len(collectionIDs) == 0 {
		return []model.CollectionVideo{}, nil
	}
	var list []model.CollectionVideo
	err := r.db.WithContext(ctx).
		Where("collection_id IN ?", collectionIDs).
		Order("collection_id ASC, sort ASC, id ASC").
		Find(&list).
		Error
	return list, err
}

// ListPublicCollectionsByVideo 返回包含该视频的公开合集，按合集创建时间倒序。
//
// 只取 status=1：草稿合集在前台等同不存在（读取侧统一按「不存在」处理）。
func (r *Repository) ListPublicCollectionsByVideo(ctx context.Context, videoID int64) ([]model.Collection, error) {
	var list []model.Collection
	err := r.db.WithContext(ctx).
		Model(&model.Collection{}).
		Joins("JOIN collection_video ON collection_video.collection_id = collection.id").
		Where("collection_video.video_id = ? AND collection.status = ?", videoID, model.CollectionStatusPublic).
		Order("collection.created_at DESC, collection.id DESC").
		Find(&list).
		Error
	return list, err
}

// PublishedCovers 返回给定视频中「已上架」记录的 id→封面地址。
//
// 返回 map 而不是切片，是因为调用方既要判断「视频是否已上架」（键是否存在），
// 又要取封面做合集封面兜底，一次查询同时满足两者。
func (r *Repository) PublishedCovers(ctx context.Context, ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	var rows []struct {
		ID       int64
		CoverURL string
	}
	err := r.db.WithContext(ctx).
		Model(&model.Video{}).
		Select("id", "cover_url").
		Where("id IN ? AND status = ?", ids, model.StatusPublished).
		Scan(&rows).
		Error
	if err != nil {
		return nil, err
	}

	covers := make(map[int64]string, len(rows))
	for _, row := range rows {
		covers[row.ID] = row.CoverURL
	}
	return covers, nil
}

// FirstPublishedCovers 返回每个合集中「第一个已上架视频」的封面，供列表页做封面兜底。
//
// 一次查询按 (collection_id, sort, id) 升序取出全部关联行与视频封面，再在内存里
// 每个合集取第一条：列表页只多一次查询，不会退化成按合集 N+1。
// 只看已上架视频——下架 / 转码中的视频本来就不在公开详情里，
// 用它们的封面兜底等于把未上架内容的画面透出去。
func (r *Repository) FirstPublishedCovers(ctx context.Context, collectionIDs []int64) (map[int64]string, error) {
	if len(collectionIDs) == 0 {
		return map[int64]string{}, nil
	}
	var rows []struct {
		CollectionID int64
		CoverURL     string
	}
	err := r.db.WithContext(ctx).
		Table("collection_video AS cv").
		Select("cv.collection_id, v.cover_url").
		Joins("JOIN video AS v ON v.id = cv.video_id").
		Where("cv.collection_id IN ? AND v.status = ?", collectionIDs, model.StatusPublished).
		Order("cv.collection_id ASC, cv.sort ASC, cv.id ASC").
		Scan(&rows).
		Error
	if err != nil {
		return nil, err
	}

	covers := make(map[int64]string, len(collectionIDs))
	for _, row := range rows {
		if _, ok := covers[row.CollectionID]; ok {
			continue
		}
		covers[row.CollectionID] = row.CoverURL
	}
	return covers, nil
}

// AddCollectionVideos 把视频按传入顺序追加到合集末尾，已存在的关联忽略。
//
// 幂等靠唯一键 uk_collection_video 与「先读已存在集合」两道保险：
// 已存在的视频既不插入、也不占用新的 sort 序号。
// 用「当前最大 sort + 1」续排而不是 COUNT(*)：中间删过视频后 COUNT 会小于最大 sort，
// 继续用 COUNT 会与现存行的 sort 撞号，让「追加到末尾」失去意义。
func (r *Repository) AddCollectionVideos(ctx context.Context, collectionID int64, videoIDs []int64) error {
	if len(videoIDs) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []int64
		if err := tx.Model(&model.CollectionVideo{}).
			Where("collection_id = ?", collectionID).
			Pluck("video_id", &existing).
			Error; err != nil {
			return err
		}
		exists := make(map[int64]struct{}, len(existing))
		for _, id := range existing {
			exists[id] = struct{}{}
		}

		// 合集为空时 COALESCE 给出 -1，从 0 开始编号，与列默认值一致。
		maxSort := int64(-1)
		if err := tx.Model(&model.CollectionVideo{}).
			Where("collection_id = ?", collectionID).
			Select("COALESCE(MAX(sort), -1)").
			Scan(&maxSort).
			Error; err != nil {
			return err
		}

		next := int(maxSort) + 1
		rows := make([]model.CollectionVideo, 0, len(videoIDs))
		for _, videoID := range videoIDs {
			if _, ok := exists[videoID]; ok {
				continue
			}
			rows = append(rows, model.CollectionVideo{
				ID:           snowflake.NextID(),
				CollectionID: collectionID,
				VideoID:      videoID,
				Sort:         next,
			})
			// 入参自身重复时服务层已拒绝，这里再兜一层：同一批里不能出现两个相同 video_id。
			exists[videoID] = struct{}{}
			next++
		}
		if len(rows) == 0 {
			return nil
		}

		// OnConflict DoNothing：与并发请求撞上同一行时以「先到者为准」，
		// 等价于「重复添加忽略」，而不是让整个请求失败。
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return err
		}
		return recountCollectionVideos(tx, collectionID)
	})
}

// RemoveCollectionVideo 从合集移除一个视频。
//
// 幂等：目标视频本来就不在合集里时视为已达成目标，直接返回成功——
// 前端重复点击或并发删除都不该报错。
func (r *Repository) RemoveCollectionVideo(ctx context.Context, collectionID, videoID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("collection_id = ? AND video_id = ?", collectionID, videoID).
			Delete(&model.CollectionVideo{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		return recountCollectionVideos(tx, collectionID)
	})
}

// ReorderCollectionVideos 按传入的完整顺序重写合集内所有关联行的 sort。
//
// 入参必须是「合集内现有视频的一个排列」：集合不一致时返回 ErrCollectionOrderMismatch，
// 由服务层翻译成参数错误。之所以要求完整集合而不是「只传要移动的项」：
// 完整顺序没有歧义，也不会因为漏传某个视频而让它意外落到末尾。
//
// 一致性判断与写 sort 在同一事务内完成，避免检查-使用竞态。
func (r *Repository) ReorderCollectionVideos(ctx context.Context, collectionID int64, orderedVideoIDs []int64) error {
	if len(orderedVideoIDs) == 0 {
		return ErrCollectionOrderMismatch
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current []int64
		if err := tx.Model(&model.CollectionVideo{}).
			Where("collection_id = ?", collectionID).
			Order("sort ASC, id ASC").
			Pluck("video_id", &current).
			Error; err != nil {
			return err
		}
		if len(current) != len(orderedVideoIDs) {
			return ErrCollectionOrderMismatch
		}
		seen := make(map[int64]struct{}, len(orderedVideoIDs))
		for _, id := range orderedVideoIDs {
			seen[id] = struct{}{}
		}
		if len(seen) != len(orderedVideoIDs) {
			return ErrCollectionOrderMismatch
		}
		for _, id := range current {
			if _, ok := seen[id]; !ok {
				return ErrCollectionOrderMismatch
			}
		}

		// 用一条 CASE WHEN 把 N 次往返压成 1 次，与 IncrViewCounts 的做法一致。
		// 值全部走占位符，不拼字符串，避免注入。
		var sb strings.Builder
		sb.WriteString("UPDATE collection_video SET sort = CASE video_id")
		args := make([]any, 0, len(orderedVideoIDs)*2+2)
		for i, videoID := range orderedVideoIDs {
			sb.WriteString(" WHEN ? THEN ?")
			args = append(args, videoID, i)
		}
		sb.WriteString(" END WHERE collection_id = ? AND video_id IN (")
		args = append(args, collectionID)

		for i, videoID := range orderedVideoIDs {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, videoID)
		}
		sb.WriteString(")")

		return tx.Exec(sb.String(), args...).Error
	})
}

// recountCollectionVideos 在事务内重算合集的冗余计数。
//
// 用全量重算而不是 ±N：添加、移出、删除视频等多条路径都会改这个计数，
// 增量一旦在任一路径漏算就会长期漂移；合集内视频数很小，重算成本可以忽略。
func recountCollectionVideos(tx *gorm.DB, collectionID int64) error {
	return tx.Model(&model.Collection{}).
		Where("id = ?", collectionID).
		UpdateColumn("video_count", gorm.Expr(
			"(SELECT COUNT(*) FROM collection_video WHERE collection_id = ?)", collectionID,
		)).
		Error
}
