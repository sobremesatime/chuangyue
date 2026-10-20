// Package service 承载视频域播放量的缓冲与批量落库。
package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// viewCountDeltaKey 是播放量增量的缓冲键（Redis Hash：字段 = 视频 ID，值 = 待落库增量）。
//
// 为什么不直接在详情接口里同步 UPDATE：详情是最高频的读接口，返回前写库意味着
// 「每个请求一次写」，当热门视频不多时所有写都落在同一行上，形成行热点（压测里详情
// P95 明显高于首页就是这么来的）。改成「请求只往 Redis 累加增量、定时任务批量落库」后，
// N 次单行 UPDATE 被合并成一批，读路径上不再有同步写。
const viewCountDeltaKey = "video:view:delta"

// takeDeltasLua 是「一次性取走全部增量」的脚本：HGETALL + DEL 必须原子完成。
//
// 若不原子，两个实例（或同一实例的两轮）可能都读到同一批增量，落库时被重复累加。
// 放在一个脚本里由 Redis 串行执行，则每一份增量只会被一个执行者取走——正因如此，
// 落库任务**不需要额外的单飞锁**：并发执行只会把工作量拆开，不会重复计数。
const takeDeltasLua = `
local pairs = redis.call('HGETALL', KEYS[1])
if #pairs == 0 then
  return pairs
end
redis.call('DEL', KEYS[1])
return pairs
`

// ViewCounter 把播放量增量缓冲在 Redis，等定时任务批量落库；无 Redis 时退化为直接写库。
type ViewCounter struct {
	rdb    *redis.Client
	repo   *repository.Repository
	script *redis.Script
}

func newViewCounter(rdb *redis.Client, repo *repository.Repository) *ViewCounter {
	return &ViewCounter{
		rdb:    rdb,
		repo:   repo,
		script: redis.NewScript(takeDeltasLua),
	}
}

// Incr 累加一次播放量。
//
// Redis 失败时不把计数丢掉，而是退回直接写库——等价于引入缓冲之前的行为。
// 宁可退回旧行为（哪怕带来行热点），也不能让播放量无声消失。
func (v *ViewCounter) Incr(ctx context.Context, id int64) error {
	err := v.rdb.HIncrBy(ctx, viewCountDeltaKey, strconv.FormatInt(id, 10), 1).Err()
	if err == nil {
		return nil
	}
	logger.L().Warn("播放量缓冲失败，退回直接写库",
		logger.TraceField(ctx), zap.Int64("video_id", id), zap.Error(err))
	return v.repo.IncrViewCount(ctx, id)
}

// FlushViewCounts 把播放量缓冲批量落库，是周期任务的入口。
//
// 未接入 Redis 时是空操作：那时播放量走的是直接写库路径，缓冲里本就没有东西可落。
func (s *QueryService) FlushViewCounts(ctx context.Context) {
	if s.viewCounter == nil {
		return
	}

	n, err := s.viewCounter.Flush(ctx)
	if err != nil {
		logger.L().Error("播放量批量落库失败", logger.TraceField(ctx), zap.Error(err))
		return
	}
	if n > 0 {
		logger.L().Info("播放量已批量落库", zap.Int("videos", n))
	}
}

// Flush 取走缓冲里的全部增量并批量落库，返回本次落库涉及（且有增量）的视频数。
func (v *ViewCounter) Flush(ctx context.Context) (int, error) {
	deltas, err := v.takeDeltas(ctx)
	if err != nil {
		return 0, fmt.Errorf("读取播放量缓冲失败: %w", err)
	}
	if len(deltas) == 0 {
		return 0, nil
	}

	if err := v.repo.IncrViewCounts(ctx, deltas); err != nil {
		// 落库失败必须把增量还回缓冲：取走与写库之间存在时间差，
		// 若不回滚，这批播放量就永久丢了（下一轮取不到）。
		v.restoreDeltas(ctx, deltas)
		return 0, fmt.Errorf("批量落库播放量失败: %w", err)
	}
	return len(deltas), nil
}

// takeDeltas 原子地读走并清空缓冲，把 Redis 的扁平返回（field, value, ...）整理成 map。
func (v *ViewCounter) takeDeltas(ctx context.Context) (map[int64]int64, error) {
	res, err := v.script.Run(ctx, v.rdb, []string{viewCountDeltaKey}).Result()
	if err != nil {
		return nil, err
	}
	flat, ok := res.([]any)
	if !ok || len(flat) == 0 {
		return nil, nil
	}

	deltas := make(map[int64]int64, len(flat)/2)
	for i := 0; i+1 < len(flat); i += 2 {
		id, errID := strconv.ParseInt(fmt.Sprint(flat[i]), 10, 64)
		delta, errDelta := strconv.ParseInt(fmt.Sprint(flat[i+1]), 10, 64)
		// 脏数据（非数字、非法 ID、增量为 0）直接跳过并告警，不因个别坏值放弃整批。
		if errID != nil || errDelta != nil || id <= 0 {
			logger.L().Warn("播放量缓冲中存在非法条目，已跳过",
				zap.String("field", fmt.Sprint(flat[i])), zap.String("value", fmt.Sprint(flat[i+1])))
			continue
		}
		deltas[id] += delta
	}
	return deltas, nil
}

// restoreDeltas 把一批增量加回缓冲（尽力而为）。
//
// 用 WithoutCancel：落库失败往往伴随 ctx 取消（进程退出 / 超时），
// 但「把增量还回去」必须完成，否则这批播放量就丢了。
func (v *ViewCounter) restoreDeltas(ctx context.Context, deltas map[int64]int64) {
	restoreCtx := context.WithoutCancel(ctx)
	for id, delta := range deltas {
		if err := v.rdb.HIncrBy(restoreCtx, viewCountDeltaKey, strconv.FormatInt(id, 10), delta).Err(); err != nil {
			logger.L().Error("播放量增量回滚失败，该条增量已丢失",
				zap.Int64("video_id", id), zap.Int64("delta", delta), zap.Error(err))
		}
	}
}
