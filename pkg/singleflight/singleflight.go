// Package singleflight 提供「同一份结果被并发重建时只算一次」的单飞机制，且**跨实例**有效。
//
// 要解决的问题是读路径上的缓存击穿与惊群：结果缓存（如首页推荐流）一旦过期或被清空，
// 同一瞬间到达的每个请求都会各自回源——同实例内是 N 个 goroutine，多副本部署时还要
// 乘以副本数 M。下游本该只承受「一次」计算，实际承受 N×M 次：候选池查询被打成放大，
// 下游越慢越容易被这一下打穿。单飞把这 N×M 收敛到 1。
//
// 两个层次叠加，缺一层都不完整：
//  1. **进程内合并**（Group）：同一实例里同一把键的并发 Do 只执行一次 build，其余等它的
//     结果。少了这层，一个实例内 100 个并发请求会变成 1 个 leader + 99 个 follower
//     超时后各自兜底回源——峰只削掉了一半。
//  2. **跨实例选主**（Flight）：用 Redis 锁（pkg/lock）选出唯一 leader 回源，其余实例在
//     waitFor 内轮询 load，复用 leader 写下的结果。少了这层，M 个副本仍会各算一遍。
//
// 关键取舍是**有界等待 + 兜底自算**：follower 不会无限期等 leader——等到 waitFor 仍读不到
// 结果（leader 太慢 / 已崩溃 / 锁已过期），就自己 build；Redis 访问失败时更是直接自算、
// 不进入等待。也就是说本机制只负责削峰，任何异常路径都退回「各算各的」，绝不把请求挂在
// leader 身上（可用性不押在选主上）。代价是 follower 最坏多等一个 waitFor，因此调用方
// 必须把它取在远小于请求超时的量级上。
//
// 与 pkg/lock 的分工：lock 回答「这件事此刻能不能做」（互斥），本包回答「这件事此刻该不该
// 由我做，不该做的人去哪拿结果」（合并）。本包内不变量：一次 Do 只可能产生 0 或 1 次 build，
// 且每个调用者拿到的都是 load 命中的值或自己 build 的值。
package singleflight

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sobremesatime/chuangyue/pkg/lock"
)

// Group 是进程内的单飞组：同一个 key 的并发调用合并成一次执行。
//
// 语义上取 golang.org/x/sync/singleflight 的最小子集——所有等待者共享同一份返回值与错误，
// 不区分「谁先来」「首个调用者的 ctx 是否已被取消」。少的那部分能力本项目用不到，
// 自己实现则省掉一个依赖，也便于在下面把「等待上限」这一层加上去。
type Group[T any] struct {
	mu    sync.Mutex
	calls map[string]*call[T]
}

// call 是一次在飞行中的调用：等待者靠 wg 同步，结果写在 val / err 上。
type call[T any] struct {
	wg  sync.WaitGroup
	val T
	err error
}

// Do 执行 fn；若此刻已有相同 key 在飞行中，则等待并复用它的结果，不再执行 fn。
//
// 注意 fn 里不应使用首个调用者的 ctx 之外的资源：等待者共享的是这一次执行的结果。
func (g *Group[T]) Do(key string, fn func() (T, error)) (T, error) {
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	if g.calls == nil {
		g.calls = make(map[string]*call[T])
	}
	c := &call[T]{}
	c.wg.Add(1)
	g.calls[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()

	// 顺序很重要：先从表中摘除再唤醒等待者。摘除后新到达的调用者会开一次新的执行
	// （若结果已写好，它在 Do 的入口就 load 命中，根本不会走到这里）；而已经拿到 c
	// 引用的等待者仍能正常等到本次结果。
	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	c.wg.Done()

	return c.val, c.err
}

// Flight 是一把键上的跨实例单飞器，泛型参数 T 是结果类型。
//
// 一个 Flight 实例可以服务任意多把键（键在 Do 时传入，锁也按键即时构造），
// 因此不必为每个用户 / 每个实体各建一个实例。
type Flight[T any] struct {
	rdb       *redis.Client // 选主用的 Redis；不可为 nil
	ttl       time.Duration // 锁租期，须大于「一次 build 的最长耗时」
	waitFor   time.Duration // follower 等 leader 写结果的上限
	pollEvery time.Duration // follower 轮询 load 的间隔
	inFlight  Group[T]      // 进程内合并
}

// New 构造单飞器。
//
//   - ttl：leader 持有单飞锁的租期。取「一次 build 的最长耗时」量级即可——它不是互斥的
//     保护期（拿不到锁的人不会等满它，只等 waitFor），而只是「leader 崩溃后多久允许别人
//     接手」的上限。
//   - waitFor：follower 愿意为「省下一次回源」付出的最大等待。必须远小于调用链的请求超时，
//     否则省了计算却把请求拖到超时。
//   - pollEvery：轮询间隔，决定「leader 写完到 follower 看见」的延迟粒度，须 > 0（取 0 会
//     退化成忙等）。
func New[T any](rdb *redis.Client, ttl, waitFor, pollEvery time.Duration) *Flight[T] {
	return &Flight[T]{rdb: rdb, ttl: ttl, waitFor: waitFor, pollEvery: pollEvery}
}

// Do 返回 key 对应的结果，保证同一份结果不会被并发重建。
//
// load 读「已算好的结果」（通常是缓存）；命中即直接返回，不触发 build、不做任何选主。
// build 计算这份结果并**自行写回**（缓存 / 存储）——必须在返回前让 load 能看到它，
// 否则等着的 follower 读不到东西，只能各自兜底回源，单飞就白做了。
//
// 同一实例内的并发 Do 由 inFlight 合并；跨实例由 Redis 锁选出唯一 leader；
// 抢不到锁的实例在 waitFor 内轮询 load，超时或 Redis 故障则自己 build（fail-open）。
func (f *Flight[T]) Do(ctx context.Context, key string, load func(context.Context) (T, bool), build func(context.Context) (T, error)) (T, error) {
	// 快路径：缓存命中时整条链路只有一次读，连进程内的单飞表都不碰。
	if v, ok := load(ctx); ok {
		return v, nil
	}

	return f.inFlight.Do(key, func() (T, error) {
		return f.electAndBuild(ctx, key, load, build)
	})
}

// electAndBuild 是单飞的实际执行体：复查 → 选主 → leader 自算 / follower 有界等待。
func (f *Flight[T]) electAndBuild(ctx context.Context, key string, load func(context.Context) (T, bool), build func(context.Context) (T, error)) (T, error) {
	// 合并后复查：在排队等前一个执行者、或抢锁的这段时间里，结果可能已经被写好了。
	if v, ok := load(ctx); ok {
		return v, nil
	}

	mu := lock.New(f.rdb, key, f.ttl)
	held, err := mu.TryLock(ctx)
	if err != nil {
		// Redis 不可用：直接自算。此时转去等待毫无意义——load 读的也是同一份 Redis，
		// 等满 waitFor 也等不到东西，只会给每个请求都加一个固定延迟，把故障放大。
		return build(ctx)
	}
	if held {
		defer func() {
			// 释放失败（租期已过、锁已被别人接管）不影响本次结果，无需处理；
			// 锁的租期本就自带兜底，最坏情况是等 ttl 后由下一个来者接手。
			_, _ = mu.Unlock(ctx)
		}()
		return build(ctx)
	}

	// 没抢到锁 = 别的实例正在重建同一份结果。等它写进 load 可见的位置：
	// 等到就不用再算一遍（多副本下这一步能省下 M-1 倍的下游读）。
	deadline := time.Now().Add(f.waitFor)
	for time.Now().Before(deadline) {
		if !sleep(ctx, f.pollEvery) {
			var zero T
			return zero, ctx.Err()
		}
		if v, ok := load(ctx); ok {
			return v, nil
		}
	}

	// 等满了还没有结果（leader 太慢，或它已经崩溃、锁要等租期才失效）：兜底自算。
	// 宁可多算一次，也不能让请求一直挂在这里——单飞的目的是削峰，不是把可用性押在 leader 上。
	return build(ctx)
}

// sleep 等待 d；期间 ctx 取消则立即返回 false，让调用者有机会退出。
//
// 不用 time.Sleep：等待发生在请求链路上，请求被取消（客户端断开 / 上游超时）时
// 必须能立刻把 ctx 的取消原因带出去，而不是白等完这一轮。
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
