// Package semaphore 提供基于 Redis 的**分布式计数信号量**（带租期的配额预约）。
//
// 它解决的问题与 pkg/lock 不同，两者不能互相替代：
//
//	互斥锁管「同一时刻只有一个人做某件事」，临界区必须短（几毫秒到几秒）；
//	计数信号量管「同一时刻最多 N 个人在做某类事」，而**占用是有生命周期的**——
//	可以跨秒、跨分钟，甚至跨越一次网络传输。
//
// 典型场景（本仓库的转码队列背压）：一次上传要先落盘 / 合并大文件，之后才登记记录、
// 投递转码任务。这中间「检查还有没有余量」与「真正占住配额」不是同一个时刻，
// 只靠「读一个计数再决定」会出现 TOCTOU——N 个上传同时收尾时都读到「还差 1 个到上限」，
// 于是全部放行，队列被超卖。互斥锁在这里不成立：临界区要横跨整段落盘耗时，
// 持有一把锁那么久会直接把上传串行化。
//
// 于是用信号量：TryAcquire 把「回收过期租约 → 判断余量 → 登记自己」三步放进**一段 Lua**
// 里原子完成，Release 交还配额；拿不到的调用方直接失败，不排队。
//
// 租期（lease）是防泄漏的关键：持有者崩溃 / 被 kill -9 时不会主动 Release，
// 若没有租期，这个配额就永久丢失——上限会一点点被幽灵占用吃掉，直到再也传不进新视频。
// 因此每次预约都带一个到期时刻，下一次 TryAcquire 会顺手回收所有已过期的预约。
//
// 与 pkg/lock 一致的语义边界：**拿不到配额不是错误**（ok=false 即可），
// Redis 访问失败才返回 err，由调用方决定 fail-open 还是 fail-closed（本包不替它判断）。
package semaphore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// acquireLua 是「先回收、再判断、再登记」的原子脚本，三步分开做就会出现 TOCTOU。
//
// 用有序集合而不是简单计数器（INCR / DECR），正是为了租期：
// 成员 = 一次预约的唯一 token，分值 = 该次预约的到期时刻（毫秒）。
// 于是「回收过期预约」就是一条按分值删范围的命令，不必逐个判断。
//
// KEYS[1] 信号量的键
// ARGV[1] 当前时刻（毫秒）
// ARGV[2] 本信号量之外、已经占用同一资源的数量（如库中已在转码的记录数）
// ARGV[3] 总量上限
// ARGV[4] 本次预约的到期时刻（毫秒）
// ARGV[5] 本次预约的唯一 token
//
// 返回 1 表示预约成功，0 表示余量已满。
const acquireLua = `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])

local used = tonumber(ARGV[2]) + redis.call('ZCARD', KEYS[1])
if used >= tonumber(ARGV[3]) then
  return 0
end

redis.call('ZADD', KEYS[1], ARGV[4], ARGV[5])
return 1
`

// releaseLua 释放一次预约。
//
// 这里**不需要**像 pkg/lock 那样先比较属主再删：锁的键上只有一个值，误删就等于删了别人的锁；
// 而有序集合里的成员本身就是 token，ZREM 只可能删掉自己那一条。
//
// KEYS[1] 信号量的键
// ARGV[1] 本次预约的 token
const releaseLua = `
return redis.call('ZREM', KEYS[1], ARGV[1])
`

// countLua 读取当前有效的预约数：先回收过期租约，再取基数。
//
// 单独写一个脚本而不是「ZRemRangeByScore + ZCard」两次调用：前者是写、后者是读，
// 分两次做会在中间被别的实例插入，读到一个既非前也非后的数。合成一段即为原子快照。
//
// KEYS[1] 信号量的键
// ARGV[1] 当前时刻（毫秒）
const countLua = `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
return redis.call('ZCARD', KEYS[1])
`

// Semaphore 是绑定「一个键 + 一个上限 + 一个租期」的计数信号量，可反复 TryAcquire。
type Semaphore struct {
	rdb     *redis.Client
	key     string
	limit   int
	lease   time.Duration
	acquire *redis.Script
	release *redis.Script
	count   *redis.Script
}

// New 构造信号量。
//
// limit 是总量上限（<=0 视为不限制，TryAcquire 永远成功，便于与「关闭背压」的配置语义对齐）；
// lease 是单次预约的租期，取值两条约束：
//   - **大于一次占用的最长耗时**：否则占用还没结束、租约先过期，余量会被重复发出去；
//   - **尽量小**：持有者崩溃后泄漏的那份配额，要到租期结束才被回收。
func New(rdb *redis.Client, key string, limit int, lease time.Duration) *Semaphore {
	return &Semaphore{
		rdb:     rdb,
		key:     key,
		limit:   limit,
		lease:   lease,
		acquire: redis.NewScript(acquireLua),
		release: redis.NewScript(releaseLua),
		count:   redis.NewScript(countLua),
	}
}

// TryAcquire 尝试占用一份配额，不等待。
//
// extra 是「本信号量之外的、已经占用同一资源的数量」——因为被数的那部分不由本包管理
// （例如转码队列：已在转码的记录在 MySQL 里，只有等在途、尚未登记的部分靠本信号量记账）。
// 余量判据是 `extra + 已预约数 < limit`，两者相加才是真实占用。
//
// 返回值：
//   - (lease, true, nil)  预约成功，用完必须 Release；
//   - (nil, false, nil)   余量已满（正常结果，不是错误），调用方应拒绝本次操作；
//   - (nil, false, err)   Redis 访问失败，调用方自行决定 fail-open / fail-closed。
func (s *Semaphore) TryAcquire(ctx context.Context, extra int) (*Lease, bool, error) {
	if s.limit <= 0 {
		// 不限制：给一个空租约，调用方无需区分「有没有真占到」。
		return &Lease{}, true, nil
	}

	now := time.Now()
	deadline := now.Add(s.lease)
	// token 只需**唯一**，不需要像 pkg/lock 那样不可猜测——那里 token 是「谁能删锁」的凭据，
	// 这里它只用来标识自己那一份预约。雪花 ID 恰好提供了跨进程的唯一性。
	token := strconv.FormatInt(snowflake.NextID(), 10)

	n, err := s.acquire.Run(ctx, s.rdb, []string{s.key},
		now.UnixMilli(), extra, s.limit, deadline.UnixMilli(), token).Int64()
	if err != nil {
		return nil, false, fmt.Errorf("预约配额失败: %w", err)
	}
	if n == 0 {
		return nil, false, nil
	}
	return &Lease{rdb: s.rdb, key: s.key, token: token, script: s.release}, true, nil
}

// Used 返回当前有效的预约数（顺带回收已过期的租约），主要用于观测与测试。
func (s *Semaphore) Used(ctx context.Context) (int, error) {
	n, err := s.count.Run(ctx, s.rdb, []string{s.key}, time.Now().UnixMilli()).Int64()
	if err != nil {
		return 0, fmt.Errorf("读取配额占用失败: %w", err)
	}
	return int(n), nil
}

// Lease 是一次成功的配额预约。
type Lease struct {
	rdb    *redis.Client
	key    string
	token  string
	script *redis.Script
}

// Release 交还配额。
//
// 已被回收（租期已过）或重复释放都返回 released=false，属正常情况而非错误——
// 与 pkg/lock 的 Unlock 同口径：不存在的成员也就等于「配额已经还回去了」。
func (l *Lease) Release(ctx context.Context) (released bool, err error) {
	if l == nil || l.key == "" {
		// 未受限（limit<=0）时空租约，无需归还。
		return false, nil
	}
	if l.token == "" {
		return false, errors.New("该预约已释放，不能重复释放")
	}

	n, err := l.script.Run(ctx, l.rdb, []string{l.key}, l.token).Int64()
	if err != nil {
		return false, fmt.Errorf("交还配额失败: %w", err)
	}
	l.token = ""
	return n == 1, nil
}
