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

// commentMaxRunes 是评论内容允许的最大字符数，与建表时的 VARCHAR(1000) 对齐。
const commentMaxRunes = 1000

// commentReplyPreviewLimit 是一级评论列表里内嵌展示的回复条数，超出的由「展开更多回复」按需拉取。
const commentReplyPreviewLimit = 2

// CommentService 承载视频评论的发表、列表与删除。
type CommentService struct {
	repo *repository.Repository
	// notifier 用于在评论被点赞 / 视频收到评论 / 评论收到回复时写站内通知，可为 nil（不通知）
	notifier *NotificationService
	// users 用于补全评论行里作者的昵称 / 头像与被回复者昵称，替代原先仓储的 JOIN user
	users UserProvider
	// videos 用于校验视频存在、补全管理端评论行的视频标题，替代原先仓储的 JOIN video
	videos VideoProvider
}

// NewCommentService 构造 CommentService。
//
// users / videos 分别为互动域取用户与视频信息的端口，由装配层注入。
func NewCommentService(repo *repository.Repository, notifier *NotificationService, users UserProvider, videos VideoProvider) *CommentService {
	return &CommentService{repo: repo, notifier: notifier, users: users, videos: videos}
}

// List 分页查询某视频下的一级评论，并内嵌每条的少量回复预览。
//
// viewerID 为当前登录用户（未登录传 0），用来标记每条评论是否已被自己点赞。
func (s *CommentService) List(ctx context.Context, videoID, viewerID int64, page, pageSize int) ([]dto.CommentInfo, int64, int, int, error) {
	if err := ensureVideoExists(ctx, s.videos, videoID); err != nil {
		return nil, 0, 0, 0, err
	}

	p, ps, offset := utils.NormalizePage(page, pageSize)
	rows, total, err := s.repo.ListComments(ctx, videoID, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	// 一次取回这批一级评论的回复预览，按 parent_id 归拢
	parentIDs := make([]int64, 0, len(rows))
	for i := range rows {
		parentIDs = append(parentIDs, rows[i].ID)
	}
	replyRows, err := s.repo.ListRepliesByParents(ctx, parentIDs, commentReplyPreviewLimit)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	// 本页一次取回作者与被回复者的展示信息并填进行结构体，再做 DTO 映射
	s.fillAuthorInfo(ctx, rows, replyRows)

	repliesOf := make(map[int64][]dto.CommentInfo, len(parentIDs))
	for i := range replyRows {
		info := toCommentInfo(&replyRows[i])
		repliesOf[replyRows[i].ParentID] = append(repliesOf[replyRows[i].ParentID], info)
	}

	list := make([]dto.CommentInfo, 0, len(rows))
	allIDs := make([]int64, 0, len(rows)+len(replyRows))
	for i := range rows {
		info := toCommentInfo(&rows[i])
		replies := repliesOf[rows[i].ID]
		if replies == nil {
			replies = make([]dto.CommentInfo, 0)
		}
		info.Replies = replies
		list = append(list, info)
		allIDs = append(allIDs, rows[i].ID)
	}
	for i := range replyRows {
		allIDs = append(allIDs, replyRows[i].ID)
	}

	// 一级评论与内嵌回复的点赞态一次查完，避免逐条判断
	liked, err := s.repo.ListLikedCommentIDs(ctx, viewerID, allIDs)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	for i := range list {
		_, list[i].Liked = liked[list[i].ID]
		for j := range list[i].Replies {
			_, list[i].Replies[j].Liked = liked[list[i].Replies[j].ID]
		}
	}
	return list, total, p, ps, nil
}

// ListReplies 分页查询某条一级评论下的回复。
//
// viewerID 为当前登录用户（未登录传 0），用来标记每条回复是否已被自己点赞。
func (s *CommentService) ListReplies(ctx context.Context, parentID, viewerID int64, page, pageSize int) ([]dto.CommentInfo, int64, int, int, error) {
	parent, err := s.repo.GetCommentByID(ctx, parentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, 0, 0, errcode.ErrCommentNotFound
	}
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if parent.Status == model.CommentStatusDeleted || parent.ParentID > 0 {
		return nil, 0, 0, 0, errcode.ErrCommentNotFound
	}

	p, ps, offset := utils.NormalizePage(page, pageSize)
	rows, total, err := s.repo.ListReplies(ctx, parentID, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	s.fillAuthorInfo(ctx, rows, nil)

	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	liked, err := s.repo.ListLikedCommentIDs(ctx, viewerID, ids)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	list := make([]dto.CommentInfo, 0, len(rows))
	for i := range rows {
		info := toCommentInfo(&rows[i])
		_, info.Liked = liked[rows[i].ID]
		list = append(list, info)
	}
	return list, total, p, ps, nil
}

// Create 发表评论或回复。
//
// parentID > 0 表示回复：指向一级评论即直接回复该评论，指向某条回复则统一扁平化到同一一级评论下，
// 并记录被回复者用于「回复 @某人」展示。
func (s *CommentService) Create(ctx context.Context, userID, videoID, parentID int64, content string) (*dto.CommentInfo, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("评论内容不能为空")
	}
	if utf8.RuneCountInString(content) > commentMaxRunes {
		return nil, errcode.ErrInvalidParam.WithMsg("评论内容过长")
	}

	if err := ensureVideoExists(ctx, s.videos, videoID); err != nil {
		return nil, err
	}

	// rootID 是最终挂载的一级评论 ID（0 表示本身即一级评论）；
	// replyToUserID 只在「回复某条回复」时记录，用于展示回复对象。
	var rootID, replyToUserID, recipientID int64
	if parentID > 0 {
		parent, err := s.repo.GetCommentByID(ctx, parentID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errcode.ErrCommentNotFound
		}
		if err != nil {
			return nil, err
		}
		if parent.Status == model.CommentStatusDeleted {
			return nil, errcode.ErrCommentNotFound
		}
		if parent.VideoID != videoID {
			return nil, errcode.ErrInvalidParam.WithMsg("被回复的评论不属于该视频")
		}
		recipientID = parent.UserID
		if parent.ParentID > 0 {
			rootID = parent.ParentID
			replyToUserID = parent.UserID
		} else {
			rootID = parent.ID
		}
	}

	c := &model.VideoComment{
		ID:            snowflake.NextID(),
		VideoID:       videoID,
		UserID:        userID,
		ParentID:      rootID,
		ReplyToUserID: replyToUserID,
		Content:       content,
		Status:        model.CommentStatusNormal,
	}
	if err := s.repo.CreateComment(ctx, c); err != nil {
		return nil, err
	}
	// 回复要维护一级评论的回复计数，前端才能正确显示「展开更多回复」
	if rootID > 0 {
		if err := s.repo.IncrementReplyCount(ctx, rootID); err != nil {
			return nil, err
		}
	}

	// 回查一次以带上作者信息，前端发表后可直接插入列表，无需再拉整页
	row, err := s.repo.GetCommentWithAuthor(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	rows := []repository.CommentWithAuthor{*row}
	s.fillAuthorInfo(ctx, rows, nil)
	if rootID > 0 {
		// 通知被回复者「你的评论收到回复」，失败不影响回复本身（best-effort）
		s.notifyCommentReplied(ctx, userID, recipientID, videoID, c.ID, content)
	} else {
		// 通知视频上传者「收到新评论」，失败不影响评论本身（best-effort）
		s.notifyVideoCommented(ctx, userID, videoID, c.ID, content)
	}

	info := toCommentInfo(&rows[0])
	return &info, nil
}

// Delete 删除评论：仅作者本人或管理员可删，采用软删保留记录。
func (s *CommentService) Delete(ctx context.Context, commentID, userID int64, isAdmin bool) error {
	c, err := s.repo.GetCommentByID(ctx, commentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errcode.ErrCommentNotFound
	}
	if err != nil {
		return err
	}
	if c.Status == model.CommentStatusDeleted {
		return errcode.ErrCommentNotFound
	}
	if c.UserID != userID && !isAdmin {
		return errcode.ErrForbidden
	}
	return s.repo.SoftDeleteComment(ctx, commentID)
}

// AdminList 分页查询全部正常评论，供管理端内容治理。
func (s *CommentService) AdminList(ctx context.Context, q videodto.AdminContentQuery) ([]videodto.AdminCommentInfo, int64, int, int, error) {
	p, ps, offset := utils.NormalizePage(q.Page, q.PageSize)
	rows, total, err := s.repo.ListCommentsAdmin(ctx, q.Keyword, offset, ps)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	fillAdminCommentUsers(ctx, s.users, rows)
	fillAdminCommentVideoTitles(ctx, s.videos, rows)

	list := make([]videodto.AdminCommentInfo, 0, len(rows))
	for i := range rows {
		list = append(list, toAdminCommentInfo(&rows[i]))
	}
	return list, total, p, ps, nil
}

// AdminDelete 管理端删除评论：评论必须存在且未被删除。
//
// 删除后给作者写一条「内容被删除」通知（best-effort）：用户看不到管理后台，
// 不通知的话评论只会凭空消失。自己删自己的内容不提醒（见 NotificationService.ContentRemoved）。
func (s *CommentService) AdminDelete(ctx context.Context, operatorID, commentID int64) error {
	c, err := s.repo.GetCommentByID(ctx, commentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errcode.ErrCommentNotFound
	}
	if err != nil {
		return err
	}
	if c.Status == model.CommentStatusDeleted {
		return errcode.ErrCommentNotFound
	}

	if err := s.repo.SoftDeleteComment(ctx, commentID); err != nil {
		return err
	}

	if s.notifier != nil {
		if err := s.notifier.ContentRemoved(ctx, model.NotifyTypeCommentRemoved,
			operatorID, c.UserID, c.VideoID, commentID, c.Content); err != nil {
			logger.L().Warn("写入评论被删除通知失败",
				logger.TraceField(ctx),
				zap.Int64("operator_id", operatorID),
				zap.Int64("comment_id", commentID),
				zap.Error(err))
		}
	}
	return nil
}

// Like 点赞评论：评论必须存在且未删除。
func (s *CommentService) Like(ctx context.Context, userID, commentID int64) (*dto.CommentLikeResult, error) {
	if err := s.ensureComment(ctx, commentID); err != nil {
		return nil, err
	}
	count, err := s.repo.LikeComment(ctx, userID, commentID)
	if err != nil {
		return nil, err
	}
	// 通知评论作者「你的评论被点赞」，失败不影响点赞本身（best-effort）
	s.notifyCommentLiked(ctx, userID, commentID)

	return &dto.CommentLikeResult{Liked: true, LikeCount: count}, nil
}

// Unlike 取消点赞评论。
func (s *CommentService) Unlike(ctx context.Context, userID, commentID int64) (*dto.CommentLikeResult, error) {
	if err := s.ensureComment(ctx, commentID); err != nil {
		return nil, err
	}
	count, err := s.repo.UnlikeComment(ctx, userID, commentID)
	if err != nil {
		return nil, err
	}
	return &dto.CommentLikeResult{Liked: false, LikeCount: count}, nil
}

// ensureComment 校验评论存在且未被删除。
func (s *CommentService) ensureComment(ctx context.Context, commentID int64) error {
	c, err := s.repo.GetCommentByID(ctx, commentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errcode.ErrCommentNotFound
	}
	if err != nil {
		return err
	}
	if c.Status == model.CommentStatusDeleted {
		return errcode.ErrCommentNotFound
	}
	return nil
}

// notifyCommentLiked 写入「评论被点赞」通知，best-effort：失败只记日志，不影响点赞结果。
func (s *CommentService) notifyCommentLiked(ctx context.Context, actorID, commentID int64) {
	if s.notifier == nil {
		return
	}
	if err := s.notifier.CommentLiked(ctx, actorID, commentID); err != nil {
		logger.L().Warn("写入评论点赞通知失败",
			logger.TraceField(ctx),
			zap.Int64("actor_id", actorID),
			zap.Int64("comment_id", commentID),
			zap.Error(err))
	}
}

// notifyVideoCommented 写入「视频收到评论」通知，best-effort：失败只记日志，不影响评论发表。
func (s *CommentService) notifyVideoCommented(ctx context.Context, actorID, videoID, commentID int64, content string) {
	if s.notifier == nil {
		return
	}
	if err := s.notifier.VideoCommented(ctx, actorID, videoID, commentID, content); err != nil {
		logger.L().Warn("写入视频评论通知失败",
			logger.TraceField(ctx),
			zap.Int64("actor_id", actorID),
			zap.Int64("video_id", videoID),
			zap.Int64("comment_id", commentID),
			zap.Error(err))
	}
}

// notifyCommentReplied 写入「评论收到回复」通知，best-effort：失败只记日志，不影响回复发表。
func (s *CommentService) notifyCommentReplied(ctx context.Context, actorID, recipientID, videoID, commentID int64, content string) {
	if s.notifier == nil {
		return
	}
	if err := s.notifier.CommentReplied(ctx, actorID, recipientID, videoID, commentID, content); err != nil {
		logger.L().Warn("写入评论回复通知失败",
			logger.TraceField(ctx),
			zap.Int64("actor_id", actorID),
			zap.Int64("recipient_id", recipientID),
			zap.Int64("comment_id", commentID),
			zap.Error(err))
	}
}

// fillAuthorInfo 补全本页评论 / 回复行的作者与被回复者展示信息。
//
// replies 可为 nil（如回复列表场景），仅 rows 需要补全。
func (s *CommentService) fillAuthorInfo(ctx context.Context, rows, replies []repository.CommentWithAuthor) {
	fillCommentUsers(ctx, s.users, rows)
	fillCommentUsers(ctx, s.users, replies)
}

// toCommentInfo 把查询结果（昵称头像已由 fillAuthorInfo 补全）转换为对外 DTO。
func toCommentInfo(row *repository.CommentWithAuthor) dto.CommentInfo {
	return dto.CommentInfo{
		ID:              row.ID,
		VideoID:         row.VideoID,
		UserID:          row.UserID,
		Username:        row.Username,
		Nickname:        row.Nickname,
		Avatar:          row.Avatar,
		Content:         row.Content,
		LikeCount:       row.LikeCount,
		CreatedAt:       row.CreatedAt,
		ParentID:        row.ParentID,
		ReplyToUserID:   row.ReplyToUserID,
		ReplyToNickname: row.ReplyToNickname,
		ReplyCount:      row.ReplyCount,
		// 始终给空数组，前端无需再判空；一级评论的真实回复在 List 里填充
		Replies: make([]dto.CommentInfo, 0),
	}
}

// toAdminCommentInfo 把管理端联表查询结果转换为对外 DTO。
func toAdminCommentInfo(row *repository.AdminCommentRow) videodto.AdminCommentInfo {
	return videodto.AdminCommentInfo{
		ID:              row.ID,
		VideoID:         row.VideoID,
		VideoTitle:      row.VideoTitle,
		UserID:          row.UserID,
		Nickname:        row.Nickname,
		Avatar:          row.Avatar,
		Content:         row.Content,
		ParentID:        row.ParentID,
		ReplyToNickname: row.ReplyToNickname,
		ReplyCount:      row.ReplyCount,
		LikeCount:       row.LikeCount,
		CreatedAt:       row.CreatedAt,
	}
}
