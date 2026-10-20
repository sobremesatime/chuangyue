// Package service 提供首页推荐流的核心逻辑。
//
// 不引入机器学习，只做三条规则：排除最近看过的、按兴趣分类加权、按分类打散；
// 候选池与用户信号分别来自 video 与 interaction 两个服务，本服务不持有任何业务表。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	interactionv1 "github.com/sobremesatime/chuangyue/api/gen/interaction/v1"
	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/feed/dto"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/singleflight"
)

const (
	// defaultPageSize 是分页大小的默认值，pageSize 非法（<= 0 或超过上限）时用它兜底。
	defaultPageSize = 20
	// maxPageSize 是分页大小上限，超过则视为非法并归一为默认值。
	maxPageSize = 100

	// minCandidatePool / maxCandidatePool 是候选池大小的下限与上限。
	// 候选池按 pageSize 的数倍取，给排除 / 重排留出冗余，避免筛选后不足一页；
	// 同时给硬上限，防止个别人用超大 pageSize 把候选池撑到接近全表。
	minCandidatePool = 60
	maxCandidatePool = 500
	// candidateFactor 是候选池相对 pageSize 的倍数。
	candidateFactor = 8

	// defaultSignalLimit 是向互动服务索取用户行为信号的条数。
	defaultSignalLimit = 200
	// interestCategoryTopN 是兴趣分类集合的容量：出现次数最多的前 N 个分类。
	interestCategoryTopN = 3
	// interestWeight 是命中兴趣分类后的加权系数。
	interestWeight = 1.5
	// maxConsecutivePerCategory 是打散时同一分类允许连续出现的上限。
	maxConsecutivePerCategory = 2

	// guestCacheKey 是未登录首页的缓存键；登录用户按 feed:home:u:<userID> 区分。
	guestCacheKey      = "feed:home:guest"
	userCacheKeyPrefix = "feed:home:u:"
	// guestCacheTTL / userCacheTTL 是缓存有效期。未登录结果与用户无关、可复用，TTL 可稍长；
	// 登录结果随用户行为变化，TTL 取短一些，让推荐更贴近最近行为。
	guestCacheTTL = 1 * time.Minute
	userCacheTTL  = 5 * time.Minute

	// fallbackCacheKey / fallbackCacheTTL 是**降级快照**：把最近一次算成功的热度榜
	// 留一份长 TTL 的副本。下游（video / interaction）整体不可用时，首页退回这份快照，
	// 而不是把 500 抛给用户。
	//
	// 为什么只存未登录结果：它是「全局热度榜」，与用户无关，发给任何用户都合适；
	// 个性化结果掺了「该用户看过什么」等信号，不该跨用户复用。
	fallbackCacheKey = "feed:home:fallback"
	fallbackCacheTTL = 24 * time.Hour

	// guestLockKey / userLockKeyPrefix 是**单飞锁**键（见 pkg/singleflight）：与结果缓存键
	// 一一对应（guest / u:<id>）但前缀独立，让「锁」与「结果」在 Redis 里一眼可分辨。
	guestLockKey      = "feed:home:lock:guest"
	userLockKeyPrefix = "feed:home:lock:u:"

	// 单飞的时间参数，三个值各有各的约束，不能混着调：
	//
	// sfLockTTL 是 leader 的锁租期。它不是互斥保护期（follower 不会等满它，只等 sfWaitFor），
	// 而只是「leader 崩溃后多久允许别人接手」的上限，故取「一次重建耗时」的量级。
	sfLockTTL = 3 * time.Second
	// sfWaitFor 是 follower 为「省下一次回源」付出的最大等待。**必须远小于网关的请求超时**：
	// 首页重建是一次候选池 RPC + 一次信号 RPC，正常在毫秒级，500ms 已足够覆盖，
	// 真等不到就自己算——省计算的前提是不能把用户请求拖到超时。
	sfWaitFor = 500 * time.Millisecond
	// sfPollEvery 是 follower 轮询缓存的间隔，决定「leader 写完 → follower 看见」的粒度。
	sfPollEvery = 50 * time.Millisecond
)

// Service 是首页推荐服务。
type Service struct {
	video       videov1.VideoServiceClient             // 候选池
	interaction interactionv1.InteractionServiceClient // 用户信号
	rdb         *redis.Client                          // 结果缓存
	// homeFlight 是首页缓存重建的单飞器：缓存未命中时，同一实例内只算一次，
	// 跨实例只由一个实例回源，其余实例短暂等待后复用结果。
	//
	// 为什么需要它：缓存过期 / 被清空的那一瞬间，N 个并发请求会同时发现「没命中」，
	// 于是各自跨 video + interaction 回源重建——同一份结果被算 N 遍（多副本还要乘 M）。
	// 缓存本身挡住了稳态流量，挡不住这一瞬间的击穿。详见 pkg/singleflight。
	homeFlight *singleflight.Flight[[]dto.VideoBrief]
}

// New 构造 Service。
func New(video videov1.VideoServiceClient, interaction interactionv1.InteractionServiceClient, rdb *redis.Client) *Service {
	return &Service{
		video:       video,
		interaction: interaction,
		rdb:         rdb,
		homeFlight:  singleflight.New[[]dto.VideoBrief](rdb, sfLockTTL, sfWaitFor, sfPollEvery),
	}
}

// Home 返回首页推荐流：登录用户个性化重排，未登录按全局热度。
//
// 返回当前页的视频与「完整结果列表」的总数（total），供前端做分页。
func (s *Service) Home(ctx context.Context, userID int64, page, pageSize int) ([]videov1.VideoBrief, int64, error) {
	page, pageSize = normalizePage(page, pageSize)

	cacheKey, lockKey := guestCacheKey, guestLockKey
	ttl := guestCacheTTL
	if userID > 0 {
		cacheKey = fmt.Sprintf("%s%d", userCacheKeyPrefix, userID)
		lockKey = fmt.Sprintf("%s%d", userLockKeyPrefix, userID)
		ttl = userCacheTTL
	}
	limit := candidatePoolSize(pageSize)

	// 整个「读缓存 → 未命中则回源重建 → 写回缓存」走单飞：
	//   - load：命中缓存直接返回（快路径，只有一次 Redis 读，不涉及锁）；
	//   - build：真的没命中才回源，且同一份结果在同一时刻只由一个执行者重建。
	// 缓存未命中于是不再是「谁来了谁算」，而是「一个算、其余复用」——这是缓存击穿那一瞬间
	// 挡住下游读放大的关键；没有它，N 个并发请求就是 N 次跨服务回源（多副本再乘 M）。
	ranked, err := s.homeFlight.Do(ctx, lockKey,
		func(ctx context.Context) ([]dto.VideoBrief, bool) { return s.loadCache(ctx, cacheKey) },
		func(ctx context.Context) ([]dto.VideoBrief, error) {
			var (
				list []dto.VideoBrief
				err  error
			)
			if userID <= 0 {
				// 未登录：不查用户信号，直接取候选池，按热度顺序作为结果。
				list, err = s.listCandidates(ctx, limit)
			} else {
				list, err = s.personalized(ctx, userID, limit)
			}
			if err != nil {
				return nil, err
			}

			// 为什么必须缓存：一次首页要跨 video + interaction 两个服务读候选池与信号，
			// 属于读放大；把重排后的完整有序列表缓存下来，后续翻页与并发请求都只读缓存。
			s.storeCache(ctx, cacheKey, list, ttl)
			// 未登录结果即「全局热度榜」，另存一份长 TTL 快照供降级使用。
			if userID <= 0 {
				s.storeCache(ctx, fallbackCacheKey, list, fallbackCacheTTL)
			}
			return list, nil
		})
	if err != nil {
		// 降级：下游（video / interaction）不可用时不再把 500 抛给用户，改为退回
		// 最近一次算成功的热度榜快照；连快照都没有（首次启动就故障）才返回空列表。
		// 首页是最高频入口，少几条内容远好于整页报错或让用户干等超时。
		//
		// 注意这与单飞的兜底是两件事，别混：「单飞」保证重建只做一次（省下游的读），
		// 「降级」保证重建失败也不报错（保用户的可用性），后者覆盖前者的失败。
		logger.L().Warn("首页推荐计算失败，降级为兜底快照", logger.TraceField(ctx), zap.Error(err))
		if list, ok := s.loadCache(ctx, fallbackCacheKey); ok {
			return toProtoPage(list, page, pageSize), int64(len(list)), nil
		}
		return []videov1.VideoBrief{}, 0, nil
	}

	return toProtoPage(ranked, page, pageSize), int64(len(ranked)), nil
}

// personalized 对候选池做个性化重排：排除看过、兴趣加权、稳定排序、分类打散。
func (s *Service) personalized(ctx context.Context, userID int64, limit int) ([]dto.VideoBrief, error) {
	candidates, err := s.listCandidates(ctx, limit)
	if err != nil {
		return nil, err
	}

	watched, engaged, err := s.signals(ctx, userID)
	if err != nil {
		return nil, err
	}

	interests, err := s.interestCategories(ctx, engaged)
	if err != nil {
		return nil, err
	}

	watchedSet := make(map[int64]struct{}, len(watched))
	for _, id := range watched {
		watchedSet[id] = struct{}{}
	}

	// 候选池天然已按热度排好，用排名分做基础分：越靠前分越高，命中兴趣分类再乘权重。
	type scored struct {
		video dto.VideoBrief
		score float64
	}
	ranked := make([]scored, 0, len(candidates))
	n := len(candidates)
	for rank, v := range candidates {
		if _, ok := watchedSet[v.ID]; ok {
			continue // 规则一：排除最近看过的
		}
		score := float64(n - rank)
		if _, ok := interests[v.CategoryID]; ok {
			score *= interestWeight // 规则二：兴趣分类加权
		}
		ranked = append(ranked, scored{video: v, score: score})
	}

	// 稳定排序：score 相同时保持候选池原有的热度顺序。
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	out := make([]dto.VideoBrief, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.video)
	}
	return diversify(out), nil
}

// listCandidates 拉取全局候选池并转换为缓存 DTO。
func (s *Service) listCandidates(ctx context.Context, limit int) ([]dto.VideoBrief, error) {
	resp, err := s.video.ListFeedCandidates(ctx, &videov1.ListFeedCandidatesRequest{Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("拉取首页候选池失败: %w", err)
	}
	videos := resp.GetVideos()
	out := make([]dto.VideoBrief, 0, len(videos))
	for _, v := range videos {
		out = append(out, *dto.FromProto(v))
	}
	return out, nil
}

// signals 拉取用户行为信号：watched 用于排除，engaged 用于算兴趣分类。
func (s *Service) signals(ctx context.Context, userID int64) (watched, engaged []int64, err error) {
	resp, err := s.interaction.GetUserSignals(ctx, &interactionv1.GetUserSignalsRequest{
		UserId: userID,
		Limit:  defaultSignalLimit,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("拉取用户行为信号失败: %w", err)
	}
	return resp.GetWatchedVideoIds(), resp.GetEngagedVideoIds(), nil
}

// interestCategories 统计 engaged 视频所属分类的出现次数，降序取前 N 个作为兴趣分类集合。
func (s *Service) interestCategories(ctx context.Context, engaged []int64) (map[int64]struct{}, error) {
	interests := make(map[int64]struct{})
	if len(engaged) == 0 {
		return interests, nil
	}

	resp, err := s.video.GetVideos(ctx, &videov1.GetVideosRequest{Ids: engaged})
	if err != nil {
		return nil, fmt.Errorf("拉取兴趣视频分类失败: %w", err)
	}

	counts := make(map[int64]int)
	for _, v := range resp.GetVideos() {
		if v.GetCategoryId() <= 0 {
			continue // 未分类不参与兴趣统计
		}
		counts[v.GetCategoryId()]++
	}

	type categoryCount struct {
		id    int64
		count int
	}
	pairs := make([]categoryCount, 0, len(counts))
	for id, c := range counts {
		pairs = append(pairs, categoryCount{id: id, count: c})
	}
	// 稳定排序，次数相同则保持 map 遍历外的既有相对顺序（这里仅需前 N 个明显高频者）。
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].count > pairs[j].count })

	for i, p := range pairs {
		if i >= interestCategoryTopN {
			break
		}
		interests[p.id] = struct{}{}
	}
	return interests, nil
}

// diversify 对有序列表打散：同一分类不允许连续出现超过 maxConsecutivePerCategory 个。
//
// 为什么需要打散：候选池按热度排序时，同题材的爆款容易扎堆排在一起，首页连续几条
// 都是同一分类会让内容显得单调；这里把会造成「同分类三连」的项后移，从后面找第一个
// 不同分类的顶上来，让相邻内容的题材交替出现。category_id 为 0（未分类）不参与约束，
// 避免把未分类视频强行拆开。
func diversify(in []dto.VideoBrief) []dto.VideoBrief {
	if len(in) <= 1 {
		return in
	}

	remaining := make([]dto.VideoBrief, len(in))
	copy(remaining, in)

	out := make([]dto.VideoBrief, 0, len(in))
	lastCategory := int64(0)
	hasLast := false
	runCount := 0

	for len(remaining) > 0 {
		pick := 0
		cat := remaining[0].CategoryID
		if hasLast && cat == lastCategory && runCount >= maxConsecutivePerCategory {
			// 当前项会造成同分类三连，跳过它，从后面找第一个不同分类的顶上来。
			pick = -1
			for i := 1; i < len(remaining); i++ {
				if remaining[i].CategoryID != lastCategory {
					pick = i
					break
				}
			}
			if pick < 0 {
				pick = 0 // 后面没有不同分类，维持原顺序追加。
			}
		}

		item := remaining[pick]
		remaining = append(remaining[:pick], remaining[pick+1:]...)
		out = append(out, item)

		if item.CategoryID == 0 {
			// 未分类不参与约束，且不计入任何分类的连续计数。
			hasLast = false
			runCount = 0
			continue
		}
		if hasLast && item.CategoryID == lastCategory {
			runCount++
		} else {
			lastCategory = item.CategoryID
			hasLast = true
			runCount = 1
		}
	}
	return out
}

// normalizePage 归一化分页参数：page < 1 归一为 1，pageSize 非法时归一为默认值。
func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > maxPageSize {
		pageSize = defaultPageSize
	}
	return page, pageSize
}

// candidatePoolSize 计算候选池大小：pageSize 的数倍，并夹在上下限之间。
func candidatePoolSize(pageSize int) int {
	size := pageSize * candidateFactor
	if size > maxCandidatePool {
		size = maxCandidatePool
	}
	if size < minCandidatePool {
		size = minCandidatePool
	}
	return size
}

// toProtoPage 从完整有序列表切出当前页并转回 proto 形态。
func toProtoPage(list []dto.VideoBrief, page, pageSize int) []videov1.VideoBrief {
	start := (page - 1) * pageSize
	if start >= len(list) {
		return []videov1.VideoBrief{}
	}
	end := start + pageSize
	if end > len(list) {
		end = len(list)
	}
	out := make([]videov1.VideoBrief, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, *list[i].ToProto())
	}
	return out
}

// loadCache 读缓存，命中返回完整有序列表。
//
// Redis 出错时降级为「未命中」（只记 warn）：首页是高频入口，宁可实时算一遍也不能让它打挂。
func (s *Service) loadCache(ctx context.Context, key string) ([]dto.VideoBrief, bool) {
	raw, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err != redis.Nil {
			logger.L().Warn("读取首页缓存失败，降级为实时计算", logger.TraceField(ctx), zap.String("key", key), zap.Error(err))
		}
		return nil, false
	}

	var list []dto.VideoBrief
	if err := json.Unmarshal(raw, &list); err != nil {
		logger.L().Warn("解析首页缓存失败，降级为实时计算", logger.TraceField(ctx), zap.String("key", key), zap.Error(err))
		return nil, false
	}
	return list, true
}

// storeCache 写缓存；失败只记 warn，不影响本次请求返回。
func (s *Service) storeCache(ctx context.Context, key string, list []dto.VideoBrief, ttl time.Duration) {
	raw, err := json.Marshal(list)
	if err != nil {
		logger.L().Warn("序列化首页缓存失败", logger.TraceField(ctx), zap.String("key", key), zap.Error(err))
		return
	}
	if err := s.rdb.Set(ctx, key, raw, ttl).Err(); err != nil {
		logger.L().Warn("写入首页缓存失败", logger.TraceField(ctx), zap.String("key", key), zap.Error(err))
	}
}
