// Package repository 负责举报表的数据访问。
package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/video/model"
)

// ReportWithContext 是举报联表查询的落点：举报本体 + 相关视频标题 + 举报人昵称。
type ReportWithContext struct {
	model.VideoReport
	VideoTitle       string `gorm:"column:video_title"`
	ReporterNickname string `gorm:"column:reporter_nickname"`
}

// reportSelect 是举报查询显式选择的列，避免 r.* 与联表列重名冲突。
const reportSelect = "r.id, r.user_id, r.target_type, r.target_id, r.video_id, r.reason, r.detail, " +
	"r.status, r.handled_by, r.handled_at, r.created_at, r.updated_at, " +
	"v.title AS video_title, u.nickname AS reporter_nickname"

// reportJoins 是举报查询固定的联表：举报人（user）+ 相关视频（video），
// 均用 LEFT JOIN，视频或举报人已删除时仍保留举报行，列表不出现空洞。
const reportJoins = "LEFT JOIN user AS u ON u.id = r.user_id " +
	"LEFT JOIN video AS v ON v.id = r.video_id"

// CreateReport 插入一条举报。
func (r *Repository) CreateReport(ctx context.Context, report *model.VideoReport) error {
	return r.db.WithContext(ctx).Create(report).Error
}

// GetReportByID 按主键查询举报，未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetReportByID(ctx context.Context, id int64) (*model.VideoReport, error) {
	var report model.VideoReport
	if err := r.db.WithContext(ctx).First(&report, id).Error; err != nil {
		return nil, err
	}
	return &report, nil
}

// GetReportWithContext 按主键查询举报并带出视频标题与举报人昵称，供发表举报后直接回传。
func (r *Repository) GetReportWithContext(ctx context.Context, id int64) (*ReportWithContext, error) {
	var row ReportWithContext
	err := r.db.WithContext(ctx).
		Table("video_report AS r").
		Joins(reportJoins).
		Select(reportSelect).
		Where("r.id = ?", id).
		Take(&row).
		Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListReports 分页查询举报，按举报时间倒序。
//
// status 为 nil 表示不按状态过滤（用指针而非数字区分「不传」与「传 0 待处理」）。
// 返回值依次为：举报列表、总数、错误。
func (r *Repository) ListReports(ctx context.Context, status *int8, offset, limit int) ([]ReportWithContext, int64, error) {
	base := r.db.WithContext(ctx).
		Table("video_report AS r").
		Joins(reportJoins)
	if status != nil {
		base = base.Where("r.status = ?", *status)
	}

	var total int64
	// 用 Session 隔离，避免 Count 产生的临时子句污染后续 Scan。
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []ReportWithContext
	err := base.
		Select(reportSelect).
		Order("r.created_at DESC, r.id DESC").
		Offset(offset).Limit(limit).
		Scan(&list).
		Error
	return list, total, err
}

// commentStatusDeleted 与互动域评论模型的「已删除」状态值一致（软删，int8 = 2）。
//
// 评论模型迁往互动域后，举报侧不再引用其常量，这里保留同值以避免视频域反向依赖互动域。
const commentStatusDeleted int8 = 2

// GetCommentVideoID 只读查询被举报评论所属的视频 ID，供举报定位评论使用。
//
// 举报原本复用评论仓储的查询；评论迁往互动域后，为保持视频域不反向依赖互动域，
// 这里直接只读查询 video_comment 表。评论不存在或已删除时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetCommentVideoID(ctx context.Context, id int64) (int64, error) {
	var row struct {
		VideoID int64 `gorm:"column:video_id"`
		Status  int8  `gorm:"column:status"`
	}
	if err := r.db.WithContext(ctx).
		Table("video_comment").
		Select("video_id, status").
		Where("id = ?", id).
		Take(&row).Error; err != nil {
		return 0, err
	}
	if row.Status == commentStatusDeleted {
		return 0, gorm.ErrRecordNotFound
	}
	return row.VideoID, nil
}

// HandleReport 记录举报的处理结果：状态、处理人与处理时间一次写入。
func (r *Repository) HandleReport(ctx context.Context, id int64, status int8, handlerID int64) error {
	return r.db.WithContext(ctx).
		Model(&model.VideoReport{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     status,
			"handled_by": handlerID,
			"handled_at": time.Now(),
		}).
		Error
}
