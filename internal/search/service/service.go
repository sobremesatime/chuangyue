// Package service 提供视频检索的核心逻辑。
//
// 检索有两条路径，由部署配置决定：
//   - ES 索引：关键词交给搜索域自己的索引，命中后只取 video_id，
//     再由视频服务批量补全卡片字段——索引不必保存播放量这类高频变化的字段；
//   - 库内 LIKE：视频服务的 ListVideos，作为 ES 未启用或不可用时的退路。
//
// 不论走哪条，对外契约（SearchVideos 的参数与响应）完全一致，
// 因此前端与网关都不感知这次演进。
package service

import (
	"context"
	"strings"

	"go.uber.org/zap"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// Searcher 是检索索引的查询能力（由 pkg/es 实现）。
//
// 抽成接口而不是直接依赖 *es.Client：service 层只需要「按关键词取一页 id」，
// 单测可以注入假实现，不必真的起一个 ES。
type Searcher interface {
	SearchIDs(ctx context.Context, keyword string, categoryID int64, from, size int) ([]int64, int64, error)
}

// Option 是 Service 的可选依赖。
type Option func(*Service)

// WithSearcher 注入检索索引；不注入表示只走视频服务的库内检索。
func WithSearcher(s Searcher) Option {
	return func(svc *Service) { svc.index = s }
}

// Service 是视频检索服务。
type Service struct {
	video videov1.VideoServiceClient
	index Searcher
}

// New 构造 Service。
func New(video videov1.VideoServiceClient, opts ...Option) *Service {
	svc := &Service{video: video}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Search 按关键词检索视频，返回当前页的精简卡片与命中总数。
//
// categoryID 与 keyword 是「与」关系，0 表示不按分类过滤。
// 关键词为空时按「不过滤关键词」处理——检索接口对参数宽容，
// 参数校验留给网关，这里不做二次报错。
func (s *Service) Search(ctx context.Context, keyword string, categoryID int64, page, pageSize int) ([]videov1.VideoBrief, int64, error) {
	kw := strings.TrimSpace(keyword)

	if s.index != nil {
		if out, total, ok := s.searchIndex(ctx, kw, categoryID, page, pageSize); ok {
			return out, total, nil
		}
		// ok=false 已在 searchIndex 里打过告警，这里继续走库内检索。
	}

	return s.searchVideoService(ctx, kw, categoryID, page, pageSize)
}

// searchIndex 走 ES 索引；返回 ok=false 表示应退回库内检索。
func (s *Service) searchIndex(ctx context.Context, keyword string, categoryID int64, page, pageSize int) ([]videov1.VideoBrief, int64, bool) {
	page, pageSize, from := utils.NormalizePage(page, pageSize)

	ids, total, err := s.index.SearchIDs(ctx, keyword, categoryID, from, pageSize)
	if err != nil {
		logger.L().Warn("检索索引不可用，退回视频服务查询", logger.TraceField(ctx), zap.Error(err))
		return nil, 0, false
	}

	// 空关键词 + 索引为空：多数是索引刚建好、回填还没完成（首次启动 / 重建）。
	// 此时若直接返回空结果，用户会看到「平台没有任何视频」；退回库内检索更安全。
	if keyword == "" && total == 0 {
		logger.L().Warn("检索索引为空，退回视频服务查询", logger.TraceField(ctx))
		return nil, 0, false
	}
	if len(ids) == 0 {
		return []videov1.VideoBrief{}, total, true
	}

	briefs, err := s.briefsByIDs(ctx, ids)
	if err != nil {
		// 索引命中了，但补全卡片失败：同样退回库内检索。
		// 取舍是「宁可慢一点、结果来自唯一真源」，也不返回只有标题的残缺卡片。
		logger.L().Warn("检索命中后补全视频信息失败，退回视频服务查询", logger.TraceField(ctx), zap.Error(err))
		return nil, 0, false
	}
	return briefs, total, true
}

// briefsByIDs 按索引给出的顺序补全卡片，并过滤掉已不可见的视频。
//
// 过滤是防御性的：下架 / 删除事件正常会在秒级内把文档从索引移除，
// 但在「索引已命中、事件还没消费」的窗口里，视频服务返回的状态才是准绳。
func (s *Service) briefsByIDs(ctx context.Context, ids []int64) ([]videov1.VideoBrief, error) {
	resp, err := s.video.GetVideos(ctx, &videov1.GetVideosRequest{Ids: ids})
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]*videov1.VideoBrief, len(resp.GetVideos()))
	for _, v := range resp.GetVideos() {
		byID[v.GetId()] = v
	}

	out := make([]videov1.VideoBrief, 0, len(ids))
	for _, id := range ids {
		v, ok := byID[id]
		if !ok || v.GetStatus() != int32(model.StatusPublished) {
			continue
		}
		out = append(out, cloneBrief(v))
	}
	return out, nil
}

// cloneBrief 逐字段拷贝卡片。
//
// 不能直接 append(*v)：protobuf 生成的结构体里含有 sync.Mutex 等运行时状态，
// 按值复制会触发 go vet 的 copylocks 检查（也确实不安全）。
func cloneBrief(v *videov1.VideoBrief) videov1.VideoBrief {
	return videov1.VideoBrief{
		Id:         v.GetId(),
		Title:      v.GetTitle(),
		UploaderId: v.GetUploaderId(),
		Status:     v.GetStatus(),
		CoverUrl:   v.GetCoverUrl(),
		CategoryId: v.GetCategoryId(),
		Duration:   v.GetDuration(),
		ViewCount:  v.GetViewCount(),
		CreatedAt:  v.GetCreatedAt(),
	}
}

// searchVideoService 是库内检索路径，也是 ES 不可用时的降级路径。
func (s *Service) searchVideoService(ctx context.Context, keyword string, categoryID int64, page, pageSize int) ([]videov1.VideoBrief, int64, error) {
	resp, err := s.video.ListVideos(ctx, &videov1.ListVideosRequest{
		Keyword:    keyword,
		CategoryId: categoryID,
		Page:       int32(page),
		PageSize:   int32(pageSize),
	})
	if err != nil {
		// 降级：检索本身依赖视频服务，它不可用时返回**空结果**而不是 500。
		// 取舍：用户看到「没有匹配结果」比看到整页报错体验好，也不会因为一个搜索框
		// 把整个页面打挂；代价是故障期间「搜不到」与「真的没有」不可区分——
		// 因此这里保留一条 Warn 日志（带 trace_id），故障排查仍可定位到具体请求。
		logger.L().Warn("检索视频失败，降级为空结果", logger.TraceField(ctx), zap.Error(err))
		return []videov1.VideoBrief{}, 0, nil
	}

	videos := resp.GetVideos()
	out := make([]videov1.VideoBrief, 0, len(videos))
	for _, v := range videos {
		out = append(out, toBrief(v))
	}
	return out, resp.GetTotal(), nil
}

// toBrief 把视频的完整信息压成契约要求的精简卡片。
//
// 检索只需要卡片字段，完整信息里的简介 / 播放地址 / 统计数不对外暴露；
// 这里逐字段拷贝而不是复用指针，避免调用方无意间改动上游响应。
func toBrief(v *videov1.VideoInfo) videov1.VideoBrief {
	return videov1.VideoBrief{
		Id:         v.GetId(),
		Title:      v.GetTitle(),
		UploaderId: v.GetUploaderId(),
		Status:     v.GetStatus(),
		CoverUrl:   v.GetCoverUrl(),
		CategoryId: v.GetCategoryId(),
		Duration:   v.GetDuration(),
		ViewCount:  v.GetViewCount(),
		CreatedAt:  v.GetCreatedAt(),
	}
}
