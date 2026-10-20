package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// reportDetailMaxRunes 是举报补充说明允许的最大字符数，与建表时的 VARCHAR(200) 对齐。
const reportDetailMaxRunes = 200

// reportReasons 是举报理由白名单，与前端下拉一一对应。
//
// 只放行白名单，避免用户传入任意文本污染管理端的筛选与统计维度。
var reportReasons = map[string]struct{}{
	"垃圾广告": {},
	"色情低俗": {},
	"违法违规": {},
	"侵权盗用": {},
	"引战辱骂": {},
	"其他":   {},
}

// ReportService 承载内容举报的发起与管理端处理。
type ReportService struct {
	repo *repository.Repository
}

// NewReportService 构造 ReportService。
func NewReportService(repo *repository.Repository) *ReportService {
	return &ReportService{repo: repo}
}

// Create 发起举报。
//
// target_type 决定被举报对象是视频还是评论：举报视频要先确认视频存在，
// 举报评论要确认评论存在且未被删，并回填评论所属视频 ID 供管理端跳转定位。
func (s *ReportService) Create(ctx context.Context, userID int64, in dto.CreateReportInput) (*dto.ReportInfo, error) {
	if in.TargetType != model.ReportTargetVideo && in.TargetType != model.ReportTargetComment {
		return nil, errcode.ErrInvalidParam.WithMsg("非法的举报对象类型")
	}
	if in.TargetID <= 0 {
		return nil, errcode.ErrInvalidParam.WithMsg("非法的举报对象 ID")
	}
	if _, ok := reportReasons[in.Reason]; !ok {
		return nil, errcode.ErrInvalidParam.WithMsg("举报理由不支持")
	}
	detail := strings.TrimSpace(in.Detail)
	if utf8.RuneCountInString(detail) > reportDetailMaxRunes {
		return nil, errcode.ErrInvalidParam.WithMsg("补充说明过长")
	}

	// videoID 定位举报对象所在视频：举报视频即视频本身，举报评论取自评论
	var videoID int64
	if in.TargetType == model.ReportTargetVideo {
		if _, err := s.repo.GetByID(ctx, in.TargetID); errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errcode.ErrVideoNotFound
		} else if err != nil {
			return nil, err
		}
		videoID = in.TargetID
	} else {
		commentVideoID, err := s.repo.GetCommentVideoID(ctx, in.TargetID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errcode.ErrCommentNotFound
		}
		if err != nil {
			return nil, err
		}
		videoID = commentVideoID
	}

	report := &model.VideoReport{
		ID:         snowflake.NextID(),
		UserID:     userID,
		TargetType: in.TargetType,
		TargetID:   in.TargetID,
		VideoID:    videoID,
		Reason:     in.Reason,
		Detail:     detail,
		Status:     model.ReportStatusPending,
	}
	if err := s.repo.CreateReport(ctx, report); err != nil {
		// 唯一键 (user_id, target_type, target_id) 冲突：同一个人对同一对象只允许举报一次
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, errcode.ErrReportDuplicated
		}
		return nil, err
	}

	// 回查一次以带上视频标题与举报人昵称，前端提交后可直接展示
	row, err := s.repo.GetReportWithContext(ctx, report.ID)
	if err != nil {
		return nil, err
	}
	info := toReportInfo(row)
	return &info, nil
}

// AdminList 分页查询举报列表，供管理端内容治理。
func (s *ReportService) AdminList(ctx context.Context, q dto.AdminReportListQuery) ([]dto.ReportInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(q.Page, q.PageSize)
	rows, total, err := s.repo.ListReports(ctx, q.Status, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	list := make([]dto.ReportInfo, 0, len(rows))
	for i := range rows {
		list = append(list, toReportInfo(&rows[i]))
	}
	return list, total, p, ps, nil
}

// Handle 处理举报：只允许「已处理」或「已忽略」，且仅待处理的举报可被处理。
func (s *ReportService) Handle(ctx context.Context, id, handlerID int64, status int8) (*dto.ReportInfo, error) {
	if status != model.ReportStatusHandled && status != model.ReportStatusIgnored {
		return nil, errcode.ErrInvalidParam.WithMsg("非法的处理结果")
	}

	report, err := s.repo.GetReportByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrReportNotFound
	}
	if err != nil {
		return nil, err
	}
	// 已处理过的举报不再重复处理，避免覆盖前一次的处理结论
	if report.Status != model.ReportStatusPending {
		return nil, errcode.ErrInvalidParam.WithMsg("该举报已处理")
	}

	if err := s.repo.HandleReport(ctx, id, status, handlerID); err != nil {
		return nil, err
	}

	row, err := s.repo.GetReportWithContext(ctx, id)
	if err != nil {
		return nil, err
	}
	info := toReportInfo(row)
	return &info, nil
}

// toReportInfo 把联表查询结果转换为对外 DTO。
func toReportInfo(row *repository.ReportWithContext) dto.ReportInfo {
	return dto.ReportInfo{
		ID:               row.ID,
		TargetType:       row.TargetType,
		TargetID:         row.TargetID,
		VideoID:          row.VideoID,
		VideoTitle:       row.VideoTitle,
		ReporterID:       row.UserID,
		ReporterNickname: row.ReporterNickname,
		Reason:           row.Reason,
		Detail:           row.Detail,
		Status:           row.Status,
		CreatedAt:        row.CreatedAt,
		HandledAt:        row.HandledAt,
	}
}
