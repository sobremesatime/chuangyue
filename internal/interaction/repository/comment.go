// Package repository 负责视频评论表的数据访问。
package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// CommentWithAuthor 是评论查询的落点：评论本体 + 作者展示信息 + 被回复者昵称。
//
// Username / Nickname / Avatar / ReplyToNickname 不再由仓储 JOIN `user` 表填充，
// 而是由 service 层通过 UserProvider 批量补全（见 service 包的 fillCommentUsers 族函数），
// 互动域因此不再跨服务边界直读用户表。
type CommentWithAuthor struct {
	model.VideoComment
	Username string `gorm:"column:username"`
	Nickname string `gorm:"column:nickname"`
	Avatar   string `gorm:"column:avatar"`
	// ReplyToNickname 是被回复者昵称，用于「回复 @某人」展示；未指定或对方已注销时为空串
	ReplyToNickname string `gorm:"column:reply_to_nickname"`
}

// commentSelect 是评论查询显式选择的列，避免 c.* 与其它列重名冲突。
const commentSelect = "c.id, c.video_id, c.user_id, c.parent_id, c.reply_to_user_id, c.content, " +
	"c.like_count, c.reply_count, c.status, c.created_at, c.updated_at"

// CreateComment 插入一条评论。
func (r *Repository) CreateComment(ctx context.Context, c *model.VideoComment) error {
	return r.db.WithContext(ctx).Create(c).Error
}

// GetCommentByID 按主键查询评论（含已软删的记录），未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetCommentByID(ctx context.Context, id int64) (*model.VideoComment, error) {
	var c model.VideoComment
	if err := r.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCommentWithAuthor 按主键查询评论并带出作者与被回复者信息，供发表评论后直接回传。
func (r *Repository) GetCommentWithAuthor(ctx context.Context, id int64) (*CommentWithAuthor, error) {
	var row CommentWithAuthor
	err := r.db.WithContext(ctx).
		Table("video_comment AS c").
		Select(commentSelect).
		Where("c.id = ?", id).
		Take(&row).
		Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListComments 分页查询某视频下的一级评论（parent_id = 0），按发表时间倒序。
//
// 回复不在这里返回：一级评论的回复由 ListRepliesByParents / ListReplies 单独查询。
// 返回值依次为：评论列表、总数、错误。
func (r *Repository) ListComments(ctx context.Context, videoID int64, offset, limit int) ([]CommentWithAuthor, int64, error) {
	base := r.db.WithContext(ctx).
		Table("video_comment AS c").
		Where("c.video_id = ?", videoID).
		Where("c.parent_id = 0").
		Where("c.status = ?", model.CommentStatusNormal)

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []CommentWithAuthor
	err := base.
		Select(commentSelect).
		Order("c.created_at DESC, c.id DESC").
		Offset(offset).Limit(limit).
		Scan(&list).
		Error
	return list, total, err
}

// ListRepliesByParents 一次性取出这批一级评论各自最早的 limit 条回复。
//
// 用窗口函数按 parent_id 分组编号后只留前 limit 条，避免为每条一级评论单独查一次（N+1）。
func (r *Repository) ListRepliesByParents(ctx context.Context, parentIDs []int64, limit int) ([]CommentWithAuthor, error) {
	if len(parentIDs) == 0 || limit <= 0 {
		return nil, nil
	}

	// 外层只取业务列，rank_no 仅用于筛掉超出的回复（不进入落点结构体）；
	// 作者与被回复者的展示信息由 service 层经 UserProvider 补全，这里不再 JOIN user。
	sql := "SELECT c.id, c.video_id, c.user_id, c.parent_id, c.reply_to_user_id, c.content, " +
		"c.like_count, c.reply_count, c.status, c.created_at, c.updated_at FROM (" +
		"SELECT c.id, ROW_NUMBER() OVER (PARTITION BY c.parent_id ORDER BY c.created_at ASC, c.id ASC) AS rank_no " +
		"FROM video_comment AS c WHERE c.parent_id IN ? AND c.status = ?" +
		") AS t " +
		"JOIN video_comment AS c ON c.id = t.id " +
		"WHERE t.rank_no <= ? " +
		"ORDER BY c.parent_id ASC, c.created_at ASC, c.id ASC"

	var list []CommentWithAuthor
	err := r.db.WithContext(ctx).Raw(sql, parentIDs, model.CommentStatusNormal, limit).Scan(&list).Error
	return list, err
}

// ListReplies 分页查询某条一级评论下的回复，按发表时间升序（先回复的在前）。
func (r *Repository) ListReplies(ctx context.Context, parentID int64, offset, limit int) ([]CommentWithAuthor, int64, error) {
	base := r.db.WithContext(ctx).
		Table("video_comment AS c").
		Where("c.parent_id = ?", parentID).
		Where("c.status = ?", model.CommentStatusNormal)

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []CommentWithAuthor
	err := base.
		Select(commentSelect).
		Order("c.created_at ASC, c.id ASC").
		Offset(offset).Limit(limit).
		Scan(&list).
		Error
	return list, total, err
}

// SoftDeleteComment 把评论标记为已删除。
//
// 删除的是回复时同步把所属一级评论的回复计数减一（下界保护，避免出现负数）。
func (r *Repository) SoftDeleteComment(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c model.VideoComment
		if err := tx.First(&c, id).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.VideoComment{}).
			Where("id = ?", id).
			Update("status", model.CommentStatusDeleted).
			Error; err != nil {
			return err
		}
		if c.ParentID <= 0 {
			return nil
		}
		return tx.Model(&model.VideoComment{}).
			Where("id = ?", c.ParentID).
			UpdateColumn("reply_count", gorm.Expr("GREATEST(reply_count - 1, 0)")).
			Error
	})
}

// IncrementReplyCount 把一级评论的回复计数加一。
func (r *Repository) IncrementReplyCount(ctx context.Context, parentID int64) error {
	return r.db.WithContext(ctx).
		Model(&model.VideoComment{}).
		Where("id = ?", parentID).
		UpdateColumn("reply_count", gorm.Expr("reply_count + 1")).
		Error
}

// LikeComment 点赞评论，返回点赞后的总数。
//
// 与视频点赞同样的做法：幂等（唯一键冲突时本次插入无行变更，跳过计数自增），
// 关系写入与计数自增放在同一事务内，避免两者不一致。
func (r *Repository) LikeComment(ctx context.Context, userID, commentID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.VideoCommentLike{
			ID:        snowflake.NextID(),
			CommentID: commentID,
			UserID:    userID,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if err := tx.Model(&model.VideoComment{}).
				Where("id = ?", commentID).
				UpdateColumn("like_count", gorm.Expr("like_count + 1")).
				Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.VideoComment{}).Where("id = ?", commentID).Pluck("like_count", &count).Error
	})
	return count, err
}

// UnlikeComment 取消点赞评论，返回取消后的总数。
//
// 幂等：没有点赞记录时直接返回当前计数；计数做下界保护，避免出现负数。
func (r *Repository) UnlikeComment(ctx context.Context, userID, commentID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("user_id = ? AND comment_id = ?", userID, commentID).
			Delete(&model.VideoCommentLike{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if err := tx.Model(&model.VideoComment{}).
				Where("id = ?", commentID).
				UpdateColumn("like_count", gorm.Expr("GREATEST(like_count - 1, 0)")).
				Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.VideoComment{}).Where("id = ?", commentID).Pluck("like_count", &count).Error
	})
	return count, err
}

// AdminCommentRow 是管理端评论治理列表的落点：评论本体 + 视频标题 + 作者信息 + 被回复者昵称。
//
// VideoTitle 由 service 层经 VideoProvider 批量补全，仓储不再 JOIN 视频表；
// Nickname / Avatar / ReplyToNickname 同样由 service 层经 UserProvider 补全。
type AdminCommentRow struct {
	model.VideoComment
	VideoTitle      string `gorm:"column:video_title"`
	Nickname        string `gorm:"column:nickname"`
	Avatar          string `gorm:"column:avatar"`
	ReplyToNickname string `gorm:"column:reply_to_nickname"`
}

// adminCommentSelect 是管理端评论列表显式选择的列。
//
// 拆库后不再 JOIN 视频表取标题：标题改由 service 层经视频服务批量补全，
// 仓储只返回评论自身字段，避免跨库查询。
const adminCommentSelect = "c.id, c.video_id, c.user_id, c.parent_id, c.reply_to_user_id, c.content, " +
	"c.like_count, c.reply_count, c.status, c.created_at, c.updated_at"

// ListCommentsAdmin 分页查询全部正常评论，供管理端内容治理，按发表时间倒序。
//
// 与 ListComments 的区别：不限定 video_id、不限定一级评论，一级评论与回复一视同仁；
// keyword 非空时按评论内容模糊匹配。返回值依次为：评论列表、总数、错误。
func (r *Repository) ListCommentsAdmin(ctx context.Context, keyword string, offset, limit int) ([]AdminCommentRow, int64, error) {
	base := r.db.WithContext(ctx).
		Table("video_comment AS c").
		Where("c.status = ?", model.CommentStatusNormal)
	if keyword != "" {
		// LIKE 的通配符需要转义，否则用户输入的 % / _ 会被当成通配符匹配到意料之外的内容
		pattern := "%" + escapeLike(keyword) + "%"
		base = base.Where("c.content LIKE ? ESCAPE '\\\\'", pattern)
	}

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []AdminCommentRow
	err := base.
		Select(adminCommentSelect).
		Order("c.created_at DESC, c.id DESC").
		Offset(offset).Limit(limit).
		Scan(&list).
		Error
	return list, total, err
}

// ListLikedCommentIDs 查询当前用户在这批评论里点过赞的评论 ID 集合。
//
// 列表页一次查完，避免为每条评论单独判断是否已点赞。
func (r *Repository) ListLikedCommentIDs(ctx context.Context, userID int64, commentIDs []int64) (map[int64]struct{}, error) {
	liked := make(map[int64]struct{})
	if userID <= 0 || len(commentIDs) == 0 {
		return liked, nil
	}

	var ids []int64
	err := r.db.WithContext(ctx).
		Model(&model.VideoCommentLike{}).
		Where("user_id = ? AND comment_id IN ?", userID, commentIDs).
		Pluck("comment_id", &ids).
		Error
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		liked[id] = struct{}{}
	}
	return liked, nil
}
