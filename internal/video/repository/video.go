// Package repository 负责视频表的数据访问。
package repository

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/video/model"
)

// Order 表示视频列表的排序方式。
type Order int

const (
	// OrderLatest 按最新发布倒序（默认）。
	OrderLatest Order = iota
	// OrderHot 按热度倒序。
	OrderHot
)

// latestOrderClause 是最新发布排序表达式。
const latestOrderClause = "created_at DESC, id DESC"

// hotOrderClause 是热度排序表达式：播放、点赞、收藏加权后，按发布时长做衰减。
//
// 用「加权分 / 时长^1.5」而不是纯计数，是为了让新内容也有机会上榜，
// 而不是让几条老爆款永久占住榜首。分母加了常数 2，避免刚发布的视频除零或瞬间冲到无穷大。
const hotOrderClause = "(view_count + like_count * 5 + favorite_count * 8) / POW(TIMESTAMPDIFF(HOUR, created_at, NOW()) + 2, 1.5) DESC, id DESC"

// orderClause 把排序枚举翻译成 SQL 排序表达式。
func orderClause(order Order) string {
	if order == OrderHot {
		return hotOrderClause
	}
	return latestOrderClause
}

// Repository 封装视频表的数据库操作。
type Repository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 插入一条视频记录。
func (r *Repository) Create(ctx context.Context, v *model.Video) error {
	return r.db.WithContext(ctx).Create(v).Error
}

// GetByID 按主键查询视频，未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetByID(ctx context.Context, id int64) (*model.Video, error) {
	var v model.Video
	if err := r.db.WithContext(ctx).First(&v, id).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// GetByIDs 批量按主键查询视频；入参为空返回空切片。
//
// 纯读操作，不存在任何副作用；返回顺序不保证与入参一致，
// 调用方如需顺序应自行按 id 归位。
func (r *Repository) GetByIDs(ctx context.Context, ids []int64) ([]model.Video, error) {
	if len(ids) == 0 {
		return []model.Video{}, nil
	}
	var list []model.Video
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListPublished 分页查询已上架视频。
//
// categoryID 为 0 表示不按分类过滤；keyword 非空时按标题或简介模糊匹配；
// order 决定排序：OrderHot 按热度公式倒序，其余按创建时间倒序。
// 返回值依次为：视频列表、总数、错误。
func (r *Repository) ListPublished(ctx context.Context, categoryID int64, keyword string, order Order, offset, limit int) ([]model.Video, int64, error) {
	query := r.db.WithContext(ctx).Model(&model.Video{}).Where("status = ?", model.StatusPublished)
	if categoryID > 0 {
		query = query.Where("category_id = ?", categoryID)
	}
	if keyword != "" {
		// LIKE 的通配符需要转义，否则用户输入的 % / _ 会被当成通配符匹配到意料之外的内容
		pattern := "%" + escapeLike(keyword) + "%"
		query = query.Where("(title LIKE ? ESCAPE '\\\\' OR description LIKE ? ESCAPE '\\\\')", pattern, pattern)
	}

	var total int64
	// 用 Session 隔离，避免 Count 产生的临时子句污染后续 Find。
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []model.Video
	if err := query.Order(orderClause(order)).Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// escapeLike 转义 LIKE 模式中的特殊字符（\ % _）。
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// defaultFeedCandidateLimit 是候选池的默认条数（入参非法时兜底）。
//
// 候选池给上限是为了避免它在库变大后无限增长：首页一次只展示十来条，
// 候选池按展示量的数倍取已足够覆盖个性化重排的筛选，取全表反而是无谓的读放大。
const defaultFeedCandidateLimit = 60

// ListFeedCandidates 取首页候选池：已上架视频按热度倒序取前 limit 条。
//
// 复用 orderClause(OrderHot) 保证与列表页热度口径一致；只取已上架（status=1），
// 转码中 / 已下架 / 失败的视频都不该出现在首页。limit <= 0 时用包级默认值。
func (r *Repository) ListFeedCandidates(ctx context.Context, limit int) ([]model.Video, error) {
	if limit <= 0 {
		limit = defaultFeedCandidateLimit
	}

	var list []model.Video
	err := r.db.WithContext(ctx).
		Model(&model.Video{}).
		Where("status = ?", model.StatusPublished).
		Order(orderClause(OrderHot)).
		Limit(limit).
		Find(&list).
		Error
	return list, err
}

// ListAll 分页查询全部视频（含未上架），按创建时间倒序。
//
// 与 ListPublished 的区别：这里不限定 status，供管理后台查看内容池全貌。
// status 为 nil 表示不按状态过滤；0 是合法状态值（转码中），
// 所以必须用指针而不是数字来区分「不传」与「传 0」。
// keyword 对标题与简介做子串匹配，空串表示不过滤。
func (r *Repository) ListAll(ctx context.Context, status *int8, keyword string, offset, limit int) ([]model.Video, int64, error) {
	query := r.db.WithContext(ctx).Model(&model.Video{})
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("title LIKE ? OR description LIKE ?", like, like)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []model.Video
	if err := query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// UpdateStatus 更新视频状态，承载管理后台的上架 / 下架。
func (r *Repository) UpdateStatus(ctx context.Context, id int64, status int8) error {
	return r.db.WithContext(ctx).
		Model(&model.Video{}).
		Where("id = ?", id).
		Update("status", status).
		Error
}

// UpdateFields 按列部分更新视频。
//
// 用 map 而不是结构体：GORM 的结构体更新会跳过零值字段，
// 那样「把分类改成 0（未分类）」这类操作会被静默丢弃。
// fields 为空时直接返回，不产生任何写操作。
func (r *Repository) UpdateFields(ctx context.Context, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Model(&model.Video{}).
		Where("id = ?", id).
		Updates(fields).
		Error
}

// UpdateTranscodeResult 回写转码结果：播放地址、时长、状态与失败原因。
//
// 由转码 Worker 调用，是异步流水线的最后一环。用 map 更新以覆盖空串
// （失败时 play_url 需要被清空）。
func (r *Repository) UpdateTranscodeResult(ctx context.Context, id int64, playURL string, duration int, status int8, errMsg string) error {
	return r.db.WithContext(ctx).
		Model(&model.Video{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"play_url":  playURL,
			"duration":  duration,
			"status":    status,
			"error_msg": errMsg,
		}).
		Error
}

// Delete 按主键物理删除视频记录。
//
// 关联数据（评论 / 点赞 / 收藏 / 弹幕 / 历史）已拆入独立库 chuangyue_interaction，
// 由视频域投递 chuangyue.video.deleted 事件、互动域异步清理（PurgeService.HandleVideoDeleted），
// 所以这里只做物理删除、不级联：互动表不在本库，级联反而越界。
func (r *Repository) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Delete(&model.Video{}, id).Error
}

// IncrViewCount 播放量自增，用于详情页统计。
func (r *Repository) IncrViewCount(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).
		Model(&model.Video{}).
		Where("id = ?", id).
		UpdateColumn("view_count", gorm.Expr("view_count + 1")).
		Error
}

// IncrViewCounts 批量累加播放量：把整批增量拼成一条 CASE WHEN 语句一次写回。
//
// 为何逐条循环 UPDATE 不行：批量落库的价值就在于把 N 次往返压成 1 次。
// 这里显式拼 SQL 而非依赖 ORM 展开切片，是为了让「哪些行、各加多少」在语句里一眼可见：
// CASE 负责给每行指定增量，WHERE id IN (...) 把更新范围收敛到这批 ID
// （若只写 CASE 不给 ELSE 且不加 WHERE，其它行的 view_count 会因 +NULL 被刷成 NULL）。
// 所有值都走占位符，不拼字符串，避免注入。
func (r *Repository) IncrViewCounts(ctx context.Context, deltas map[int64]int64) error {
	if len(deltas) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("UPDATE video SET view_count = view_count + CASE id")
	args := make([]any, 0, len(deltas)*3)
	for id, delta := range deltas {
		sb.WriteString(" WHEN ? THEN ?")
		args = append(args, id, delta)
	}
	sb.WriteString(" END WHERE id IN (")

	first := true
	for id := range deltas {
		if !first {
			sb.WriteString(",")
		}
		sb.WriteString("?")
		args = append(args, id)
		first = false
	}
	sb.WriteString(")")

	return r.db.WithContext(ctx).Exec(sb.String(), args...).Error
}

// CountByStatus 统计处于指定状态的视频数。
//
// 目前用于上传背压：统计「转码中」的记录数即队列深度（见 service.reserveTranscodeSlot）。
// 注意它只反映**已登记**的在转数，不含「已放行但还没登记」的在途上传——
// 那部分由 pkg/semaphore 的预约数补齐，两者相加才是真实占用。
func (r *Repository) CountByStatus(ctx context.Context, status int8) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.Video{}).
		Where("status = ?", status).
		Count(&count).
		Error
	return count, err
}

// UpdateInteractionCounts 用互动域回写的全量计数刷新视频上的计数副本。
//
// 拆库后点赞 / 收藏计数的真源在互动域；这里的 like_count / favorite_count 只是
// 供热度排序的只读副本，由 video.stat 主题异步回写，允许短暂滞后。
// 写「全量快照」而非增量，保证消费幂等：重复消费只是覆写成同一个值。
func (r *Repository) UpdateInteractionCounts(ctx context.Context, id, likeCount, favoriteCount int64) error {
	return r.db.WithContext(ctx).
		Model(&model.Video{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"like_count":     likeCount,
			"favorite_count": favoriteCount,
		}).
		Error
}
