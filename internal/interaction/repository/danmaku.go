// Package repository 负责视频弹幕表的数据访问。
package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/interaction/model"
)

// DanmakuWithAuthor 是弹幕查询的落点：弹幕本体 + 发送者展示信息。
//
// Username / Nickname / Avatar 由 service 层经 UserProvider 补全，仓储不再 JOIN `user`。
type DanmakuWithAuthor struct {
	model.VideoDanmaku
	Username string `gorm:"column:username"`
	Nickname string `gorm:"column:nickname"`
	Avatar   string `gorm:"column:avatar"`
}

// danmakuSelect 是弹幕查询显式选择的列，避免 d.* 与其它列重名冲突。
const danmakuSelect = "d.id, d.video_id, d.user_id, d.content, d.time_ms, d.color, d.mode, " +
	"d.status, d.created_at, d.updated_at"

// CreateDanmaku 插入一条弹幕。
func (r *Repository) CreateDanmaku(ctx context.Context, d *model.VideoDanmaku) error {
	return r.db.WithContext(ctx).Create(d).Error
}

// GetDanmakuByID 按主键查询弹幕（含已软删的记录），未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetDanmakuByID(ctx context.Context, id int64) (*model.VideoDanmaku, error) {
	var d model.VideoDanmaku
	if err := r.db.WithContext(ctx).First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDanmakuWithAuthor 按主键查询弹幕并带出发送者信息，供发表后直接回传。
func (r *Repository) GetDanmakuWithAuthor(ctx context.Context, id int64) (*DanmakuWithAuthor, error) {
	var row DanmakuWithAuthor
	err := r.db.WithContext(ctx).
		Table("video_danmaku AS d").
		Select(danmakuSelect).
		Where("d.id = ?", id).
		Take(&row).
		Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListDanmaku 查询某视频下的全部正常弹幕，按时间轴升序。
//
// 当前版本一次性返回全部弹幕，由前端按播放进度择时展示；
// 后续版本数据量上来后改为按时间窗（time_ms 区间）分段拉取。
func (r *Repository) ListDanmaku(ctx context.Context, videoID int64) ([]DanmakuWithAuthor, error) {
	var list []DanmakuWithAuthor
	err := r.db.WithContext(ctx).
		Table("video_danmaku AS d").
		Where("d.video_id = ?", videoID).
		Where("d.status = ?", model.DanmakuStatusNormal).
		Select(danmakuSelect).
		Order("d.time_ms ASC, d.id ASC").
		Scan(&list).
		Error
	return list, err
}

// AdminDanmakuRow 是管理端弹幕治理列表的落点：弹幕本体 + 视频标题 + 发送者昵称。
//
// VideoTitle / Nickname 由 service 层分别经 VideoProvider / UserProvider 批量补全，
// 仓储不再 JOIN 视频表或 `user` 表。
type AdminDanmakuRow struct {
	model.VideoDanmaku
	VideoTitle string `gorm:"column:video_title"`
	Nickname   string `gorm:"column:nickname"`
}

// adminDanmakuSelect 是管理端弹幕列表显式选择的列；标题交由 service 层补全，
// 仓储只返回弹幕自身字段，避免跨库查询。
const adminDanmakuSelect = "d.id, d.video_id, d.user_id, d.content, d.time_ms, d.color, d.mode, " +
	"d.status, d.created_at, d.updated_at"

// ListDanmakuAdmin 分页查询全部正常弹幕，供管理端内容治理，按发表时间倒序。
//
// keyword 非空时按弹幕内容模糊匹配。返回值依次为：弹幕列表、总数、错误。
func (r *Repository) ListDanmakuAdmin(ctx context.Context, keyword string, offset, limit int) ([]AdminDanmakuRow, int64, error) {
	base := r.db.WithContext(ctx).
		Table("video_danmaku AS d").
		Where("d.status = ?", model.DanmakuStatusNormal)
	if keyword != "" {
		// LIKE 的通配符需要转义，否则用户输入的 % / _ 会被当成通配符匹配到意料之外的内容
		pattern := "%" + escapeLike(keyword) + "%"
		base = base.Where("d.content LIKE ? ESCAPE '\\\\'", pattern)
	}

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []AdminDanmakuRow
	err := base.
		Select(adminDanmakuSelect).
		Order("d.created_at DESC, d.id DESC").
		Offset(offset).Limit(limit).
		Scan(&list).
		Error
	return list, total, err
}

// SoftDeleteDanmaku 把弹幕标记为已删除。
func (r *Repository) SoftDeleteDanmaku(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).
		Model(&model.VideoDanmaku{}).
		Where("id = ?", id).
		Update("status", model.DanmakuStatusDeleted).
		Error
}
