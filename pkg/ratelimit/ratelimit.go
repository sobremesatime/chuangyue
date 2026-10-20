// Package ratelimit 提供基于 Redis 的**令牌桶**限流。
//
// 为什么用令牌桶：它同时表达「平均速率」与「突发额度」——桶容量是允许的瞬时突发，
// 补充速率是长期平均速率。这比固定窗口更贴合真实流量（不会在窗口边界被放大到两倍）。
//
// 为什么用 Redis + Lua：限流状态必须在多个网关副本之间共享（否则 N 个副本就是 N 倍额度），
// 而「补充令牌 + 判定 + 扣减」必须原子完成，所以整段逻辑放在一个 Lua 脚本里一次往返执行。
//
// 失败策略是 **fail-open**：Redis 报错时调用方应放行（见 pkg/middleware.RateLimit）。
// 限流是保护措施，不是业务功能——保护组件自己挂了不该把站点一起带走。
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBucketLua 是令牌桶的核心逻辑，一次往返完成「补充 + 判定 + 扣减 + 续期」。
//
// KEYS[1] 桶的键
// ARGV[1] capacity 桶容量（允许的突发额度）
// ARGV[2] rate     补充速率（每秒新增令牌数）
// ARGV[3] now      当前时间（毫秒）
// ARGV[4] need     本次要消耗的令牌数
// 返回 { 是否放行, 剩余令牌 }（都用整数表达，避免 Redis 返回浮点带来精度歧义）
const tokenBucketLua = `
local capacity = tonumber(ARGV[1])
local rate     = tonumber(ARGV[2])
local now      = tonumber(ARGV[3])
local need     = tonumber(ARGV[4])

local data   = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])

-- 首次访问：桶是满的
if tokens == nil then
  tokens = capacity
  ts = now
end

-- 按流逝时间补充令牌（上限为桶容量）
local elapsed = math.max(0, now - ts) / 1000
tokens = math.min(capacity, tokens + elapsed * rate)

local allowed = 0
if tokens >= need then
  tokens = tokens - need
  allowed = 1
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now)
-- 桶从空补满所需时间 + 余量作为 TTL，避免闲置 key 永久驻留
local ttl = math.ceil(capacity / rate) + 60
redis.call('EXPIRE', KEYS[1], ttl)

return { allowed, math.floor(tokens) }
`

// Limiter 是「一个维度」的令牌桶限流器（如按 IP、按用户）。
type Limiter struct {
	rdb      *redis.Client
	prefix   string
	capacity int
	rate     float64
	script   *redis.Script
}

// New 构造限流器。
//
// prefix 用于隔离不同维度（如 "rl:ip:" / "rl:user:"），避免不同维度共用同一个桶。
// capacity / perSecond 非法（<= 0）时按 1 处理，避免出现「零额度」把站点全限死。
func New(rdb *redis.Client, prefix string, capacity int, perSecond float64) *Limiter {
	if capacity <= 0 {
		capacity = 1
	}
	if perSecond <= 0 {
		perSecond = 1
	}
	return &Limiter{
		rdb:      rdb,
		prefix:   prefix,
		capacity: capacity,
		rate:     perSecond,
		script:   redis.NewScript(tokenBucketLua),
	}
}

// Result 是一次限流判定的结果。
type Result struct {
	// Allowed 为 false 表示本次请求超出额度，应拒绝。
	Allowed bool
	// Remaining 是判定后桶里剩余的令牌数（可用于返回 Retry-After 之类的提示）。
	Remaining int
}

// Allow 对某个 key 消耗一个令牌。
//
// key 由调用方按维度拼装（如客户端 IP、用户 ID）。返回 error 表示限流组件自身故障，
// 调用方应据此**放行**（fail-open）。
func (l *Limiter) Allow(ctx context.Context, key string) (Result, error) {
	now := time.Now().UnixMilli()
	raw, err := l.script.Run(ctx, l.rdb, []string{l.prefix + key}, l.capacity, l.rate, now, 1).Result()
	if err != nil {
		return Result{}, fmt.Errorf("执行限流脚本失败: %w", err)
	}

	vals, ok := raw.([]any)
	if !ok || len(vals) < 2 {
		return Result{}, fmt.Errorf("限流脚本返回格式异常: %v", raw)
	}
	allowed, _ := vals[0].(int64)
	remaining, _ := vals[1].(int64)
	return Result{Allowed: allowed == 1, Remaining: int(remaining)}, nil
}
