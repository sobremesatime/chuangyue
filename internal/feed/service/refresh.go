// Package service 承载推荐域的周期任务逻辑。
package service

import (
	"context"

	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// RefreshRanking 预热首页的全局热度榜缓存，是周期任务的入口。
//
// 未登录首页（全局热度榜）原本只在缓存过期后的第一个请求上实时计算：那一次请求要跨
// video / interaction 两个服务，既是延迟尖峰，也是一个缓存击穿窗口。周期任务按缓存 TTL
// 的节奏提前算好写回，读请求因此恒为「一次 Redis 读」；顺带把降级快照刷成最新，
// 下游真出故障时兜底内容不至于是一份很旧的榜单。
//
// 与 Home 的未登录分支保持同一口径：同样的候选池大小（默认分页算出的池大小）、
// 同样的候选池顺序，不额外做个性化重排。
//
// **为什么这里不走 pkg/singleflight 的重建通道**：单飞的入口是「先 load，命中就返回」，
// 而预热的本意恰恰是「不管缓存里那份是否还在有效期，都要换成最新的」——套进单飞就把
// 强制刷新降级成了「命中即跳过」，预热的意义就没了。任务本身已有单飞锁（见 cmd/feed 的
// WithLock），多副本下同一轮只有一个实例真正回源；它与请求驱动的重建可能同时发生
// （缓存刚过期的那一瞬间），但两者写入的是同一份候选池、内容等价，覆盖不产生正确性问题。
//
// 失败只记 warn：预热失败不影响任何对外语义——缓存过期后请求自会实时回源，
// 与没有这个任务时完全一致。
func (s *Service) RefreshRanking(ctx context.Context) {
	ranked, err := s.listCandidates(ctx, candidatePoolSize(defaultPageSize))
	if err != nil {
		logger.L().Warn("预热首页热度榜失败", logger.TraceField(ctx), zap.Error(err))
		return
	}

	s.storeCache(ctx, guestCacheKey, ranked, guestCacheTTL)
	s.storeCache(ctx, fallbackCacheKey, ranked, fallbackCacheTTL)
	logger.L().Info("已刷新首页热度榜", logger.TraceField(ctx), zap.Int("count", len(ranked)))
}
