package service

import (
	"context"

	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/internal/interaction/dto"
	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// VideoProvider 提供互动域所需的视频信息，屏蔽「跨服务 gRPC」这一来源。
//
// 物理拆库后互动域不再拥有视频表：校验视频是否存在、取卡片字段、取管理端所需
// 的视频标题，全部经本端口向视频服务索取。与 UserProvider 是同一套路——
// 互动域只依赖接口，不感知视频域的实现，也不再触碰视频库。
type VideoProvider interface {
	// GetVideo 返回单个视频信息；视频不存在时返回 (nil, nil)，由调用方决定如何报错。
	GetVideo(ctx context.Context, id int64) (*dto.VideoInfo, error)
	// GetVideos 批量返回视频信息；不存在的 id 不出现在结果里。返回 map 便于按 id 组装。
	GetVideos(ctx context.Context, ids []int64) (map[int64]dto.VideoInfo, error)
}

// ensureVideoExists 校验视频存在，不存在时返回 errcode.ErrVideoNotFound。
//
// 供评论 / 弹幕 / 互动等需要「视频必须存在」的场景复用；已下架的视频仍视为存在
// （允许在其上保留 / 新增互动，只是列表不再展示）。
func ensureVideoExists(ctx context.Context, provider VideoProvider, videoID int64) error {
	info, err := provider.GetVideo(ctx, videoID)
	if err != nil {
		return err
	}
	if info == nil {
		return errcode.ErrVideoNotFound
	}
	return nil
}

// loadVideoMap 调一次 VideoProvider 取回这批 id 的视频信息。
//
// 与 loadUsers 的「best-effort」不同：视频信息是列表/通知的主数据，视频服务不可用时
// 不应静默降级成空列表（那会让用户以为数据丢了），因此这里把错误上抛给调用方。
func loadVideoMap(ctx context.Context, provider VideoProvider, ids []int64) (map[int64]dto.VideoInfo, error) {
	if provider == nil || len(ids) == 0 {
		return nil, nil
	}
	return provider.GetVideos(ctx, ids)
}

// fillAdminCommentVideoTitles 批量补全管理端评论行的视频标题（best-effort）。
//
// 标题是辅助展示：视频服务抖动时留空即可，不因为一个辅助字段把整页治理列表打挂。
func fillAdminCommentVideoTitles(ctx context.Context, provider VideoProvider, rows []repository.AdminCommentRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].VideoID)
	}
	videos := loadVideoTitles(ctx, provider, ids)
	for i := range rows {
		if v, ok := videos[rows[i].VideoID]; ok {
			rows[i].VideoTitle = v.Title
		}
	}
}

// fillAdminDanmakuVideoTitles 批量补全管理端弹幕行的视频标题（best-effort）。
func fillAdminDanmakuVideoTitles(ctx context.Context, provider VideoProvider, rows []repository.AdminDanmakuRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].VideoID)
	}
	videos := loadVideoTitles(ctx, provider, ids)
	for i := range rows {
		if v, ok := videos[rows[i].VideoID]; ok {
			rows[i].VideoTitle = v.Title
		}
	}
}

// fillNotificationVideoTitles 批量补全通知行的视频标题（best-effort）。
func fillNotificationVideoTitles(ctx context.Context, provider VideoProvider, rows []repository.NotificationWithActor) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].VideoID)
	}
	videos := loadVideoTitles(ctx, provider, ids)
	for i := range rows {
		if v, ok := videos[rows[i].VideoID]; ok {
			rows[i].VideoTitle = v.Title
		}
	}
}

// loadVideoTitles 取回这批 id 的视频信息用于补全标题，出错时只记警告并返回空表。
func loadVideoTitles(ctx context.Context, provider VideoProvider, ids []int64) map[int64]dto.VideoInfo {
	if provider == nil || len(ids) == 0 {
		return nil
	}
	videos, err := provider.GetVideos(ctx, ids)
	if err != nil {
		logger.L().Warn("批量获取视频信息失败，相关视频标题留空", logger.TraceField(ctx), zap.Error(err))
		return nil
	}
	return videos
}
