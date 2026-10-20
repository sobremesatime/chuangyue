package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/model"
	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
	videodto "github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// danmakuMaxRunes 是弹幕内容允许的最大字符数，与建表时的 VARCHAR(100) 对齐。
const danmakuMaxRunes = 100

// danmakuColors 是弹幕允许的文字颜色白名单，与前端调色板一一对应。
//
// 只放行白名单，避免用户传入任意字符串把播放器样式撑坏。
var danmakuColors = map[string]struct{}{
	"#FFFFFF": {},
	"#8B5CF6": {},
	"#EF4444": {},
	"#F59E0B": {},
	"#10B981": {},
	"#3B82F6": {},
}

// DanmakuService 承载视频弹幕的发表、拉取与删除。
type DanmakuService struct {
	repo *repository.Repository
	// notifier 用于在弹幕被管理员删除时通知发送者，可为 nil（不通知）
	notifier *NotificationService
	// users 用于补全弹幕发送者的展示信息，替代原先仓储的 JOIN user
	users UserProvider
	// videos 用于校验视频存在、补全管理端弹幕行的视频标题，替代原先仓储的 JOIN video
	videos VideoProvider
}

// NewDanmakuService 构造 DanmakuService。
//
// users / videos 分别为互动域取用户与视频信息的端口，由装配层注入。
func NewDanmakuService(repo *repository.Repository, notifier *NotificationService, users UserProvider, videos VideoProvider) *DanmakuService {
	return &DanmakuService{repo: repo, notifier: notifier, users: users, videos: videos}
}

// List 拉取某视频下的全部正常弹幕（按时间轴升序）。
func (s *DanmakuService) List(ctx context.Context, videoID int64) ([]dto.DanmakuInfo, error) {
	if err := ensureVideoExists(ctx, s.videos, videoID); err != nil {
		return nil, err
	}

	rows, err := s.repo.ListDanmaku(ctx, videoID)
	if err != nil {
		return nil, err
	}
	fillDanmakuUsers(ctx, s.users, rows)

	list := make([]dto.DanmakuInfo, 0, len(rows))
	for i := range rows {
		list = append(list, toDanmakuInfo(&rows[i]))
	}
	return list, nil
}

// Create 发表弹幕。
func (s *DanmakuService) Create(ctx context.Context, userID, videoID int64, in dto.CreateDanmakuInput) (*dto.DanmakuInfo, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("弹幕内容不能为空")
	}
	if utf8.RuneCountInString(content) > danmakuMaxRunes {
		return nil, errcode.ErrInvalidParam.WithMsg("弹幕内容过长")
	}
	if in.TimeMs < 0 {
		return nil, errcode.ErrInvalidParam.WithMsg("弹幕时间点非法")
	}

	color := strings.ToUpper(strings.TrimSpace(in.Color))
	if color == "" {
		color = "#FFFFFF"
	}
	if _, ok := danmakuColors[color]; !ok {
		return nil, errcode.ErrInvalidParam.WithMsg("弹幕颜色不支持")
	}

	mode := in.Mode
	if mode == 0 {
		mode = model.DanmakuModeScroll
	}
	if mode != model.DanmakuModeScroll && mode != model.DanmakuModeTop && mode != model.DanmakuModeBottom {
		return nil, errcode.ErrInvalidParam.WithMsg("弹幕模式不支持")
	}

	if err := ensureVideoExists(ctx, s.videos, videoID); err != nil {
		return nil, err
	}

	d := &model.VideoDanmaku{
		ID:      snowflake.NextID(),
		VideoID: videoID,
		UserID:  userID,
		Content: content,
		TimeMs:  in.TimeMs,
		Color:   color,
		Mode:    mode,
		Status:  model.DanmakuStatusNormal,
	}
	if err := s.repo.CreateDanmaku(ctx, d); err != nil {
		return nil, err
	}

	// 回查一次以带上发送者信息，前端发表后可直接上屏，无需再拉整份弹幕
	row, err := s.repo.GetDanmakuWithAuthor(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	rows := []repository.DanmakuWithAuthor{*row}
	fillDanmakuUsers(ctx, s.users, rows)
	info := toDanmakuInfo(&rows[0])
	return &info, nil
}

// Delete 删除弹幕：仅发送者本人或管理员可删，采用软删保留记录。
func (s *DanmakuService) Delete(ctx context.Context, danmakuID, userID int64, isAdmin bool) error {
	d, err := s.repo.GetDanmakuByID(ctx, danmakuID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errcode.ErrDanmakuNotFound
	}
	if err != nil {
		return err
	}
	if d.Status == model.DanmakuStatusDeleted {
		return errcode.ErrDanmakuNotFound
	}
	if d.UserID != userID && !isAdmin {
		return errcode.ErrForbidden
	}
	return s.repo.SoftDeleteDanmaku(ctx, danmakuID)
}

// AdminList 分页查询全部正常弹幕，供管理端内容治理。
func (s *DanmakuService) AdminList(ctx context.Context, q videodto.AdminContentQuery) ([]videodto.AdminDanmakuInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(q.Page, q.PageSize)
	rows, total, err := s.repo.ListDanmakuAdmin(ctx, q.Keyword, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	fillAdminDanmakuUsers(ctx, s.users, rows)
	fillAdminDanmakuVideoTitles(ctx, s.videos, rows)

	list := make([]videodto.AdminDanmakuInfo, 0, len(rows))
	for i := range rows {
		list = append(list, toAdminDanmakuInfo(&rows[i]))
	}
	return list, total, p, ps, nil
}

// AdminDelete 管理端删除弹幕：弹幕必须存在且未被删除。
//
// 删除后给发送者写一条「内容被删除」通知（best-effort），否则弹幕只会凭空消失。
func (s *DanmakuService) AdminDelete(ctx context.Context, operatorID, danmakuID int64) error {
	d, err := s.repo.GetDanmakuByID(ctx, danmakuID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errcode.ErrDanmakuNotFound
	}
	if err != nil {
		return err
	}
	if d.Status == model.DanmakuStatusDeleted {
		return errcode.ErrDanmakuNotFound
	}
	if err := s.repo.SoftDeleteDanmaku(ctx, danmakuID); err != nil {
		return err
	}

	if s.notifier != nil {
		if err := s.notifier.ContentRemoved(ctx, model.NotifyTypeDanmakuRemoved,
			operatorID, d.UserID, d.VideoID, danmakuID, d.Content); err != nil {
			logger.L().Warn("写入弹幕被删除通知失败",
				logger.TraceField(ctx),
				zap.Int64("operator_id", operatorID),
				zap.Int64("danmaku_id", danmakuID),
				zap.Error(err))
		}
	}
	return nil
}

// toDanmakuInfo 把查询结果（发送者信息已由 fillDanmakuUsers 补全）转换为对外 DTO。
func toDanmakuInfo(row *repository.DanmakuWithAuthor) dto.DanmakuInfo {
	return dto.DanmakuInfo{
		ID:        row.ID,
		VideoID:   row.VideoID,
		UserID:    row.UserID,
		Username:  row.Username,
		Nickname:  row.Nickname,
		Avatar:    row.Avatar,
		Content:   row.Content,
		TimeMs:    row.TimeMs,
		Color:     row.Color,
		Mode:      row.Mode,
		CreatedAt: row.CreatedAt,
	}
}

// toAdminDanmakuInfo 把管理端联表查询结果转换为对外 DTO。
func toAdminDanmakuInfo(row *repository.AdminDanmakuRow) videodto.AdminDanmakuInfo {
	return videodto.AdminDanmakuInfo{
		ID:         row.ID,
		VideoID:    row.VideoID,
		VideoTitle: row.VideoTitle,
		UserID:     row.UserID,
		Nickname:   row.Nickname,
		Content:    row.Content,
		TimeMs:     row.TimeMs,
		Mode:       row.Mode,
		CreatedAt:  row.CreatedAt,
	}
}
