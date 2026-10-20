package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/sobremesatime/chuangyue/internal/video/model"
)

// CreateSession 插入一条上传会话。
func (r *Repository) CreateSession(ctx context.Context, s *model.UploadSession) error {
	return r.db.WithContext(ctx).Create(s).Error
}

// GetSession 按主键查询上传会话，未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetSession(ctx context.Context, id int64) (*model.UploadSession, error) {
	var s model.UploadSession
	if err := r.db.WithContext(ctx).First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// GetSessionByMD5 按文件指纹与状态查询会话，用于秒传与断点续传。
//
// 同一 MD5 可能先后存在多条记录（例如上一次已中止），这里取最新一条。
func (r *Repository) GetSessionByMD5(ctx context.Context, md5 string, status int8) (*model.UploadSession, error) {
	var s model.UploadSession
	if err := r.db.WithContext(ctx).
		Where("file_md5 = ? AND status = ?", md5, status).
		Order("created_at DESC, id DESC").
		First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateSessionFields 按列部分更新会话。
//
// 用 map 更新以覆盖零值（例如把 video_id 写成具体值、把状态改为已完成）。
func (r *Repository) UpdateSessionFields(ctx context.Context, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where("id = ?", id).
		Updates(fields).
		Error
}

// UpsertPart 记录一个已上传分片，重复上传时覆盖。
//
// 主键 (session_id, part_number) 天然去重，因此重传同一分片不会报错。
func (r *Repository) UpsertPart(ctx context.Context, p *model.UploadPart) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "session_id"}, {Name: "part_number"}},
			DoUpdates: clause.AssignmentColumns([]string{"etag", "size", "created_at"}),
		}).
		Create(p).
		Error
}

// ListParts 按分片序号升序返回会话下已上传的分片。
func (r *Repository) ListParts(ctx context.Context, sessionID int64) ([]model.UploadPart, error) {
	var parts []model.UploadPart
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("part_number ASC").
		Find(&parts).Error; err != nil {
		return nil, err
	}
	return parts, nil
}

// ListExpiredSessions 查询已过期但仍处于「上传中」的会话，供清理任务回收。
//
// 按过期时间升序取前 limit 条，先过期的先处理；已完成的会话不在此列，
// 因此秒传所依赖的记录不会被误清。
func (r *Repository) ListExpiredSessions(ctx context.Context, now time.Time, limit int) ([]model.UploadSession, error) {
	var sessions []model.UploadSession
	if err := r.db.WithContext(ctx).
		Where("status = ? AND expires_at < ?", model.UploadStatusUploading, now).
		Order("expires_at ASC").
		Limit(limit).
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// DeleteSessionParts 删除一个会话下的全部分片明细。
func (r *Repository) DeleteSessionParts(ctx context.Context, sessionID int64) error {
	return r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Delete(&model.UploadPart{}).
		Error
}

// DeleteSession 删除上传会话记录。
func (r *Repository) DeleteSession(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Delete(&model.UploadSession{}, id).Error
}
