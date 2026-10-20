// Package lock 提供基于 Redis 的**分布式互斥锁**。
//
// 为什么需要它：单进程部署时，「同一时刻只该有一个执行者」用进程内的互斥（sync.Mutex）
// 或单实例编排就够了；一旦同一服务多副本部署（如 K8s prod 下 video / feed 各 2 副本），
// 就出现了**同一件事被 N 个实例同时做**的问题——每小时的过期上传清理、缓存重建、
// 定时任务等等。这类「单飞」语义需要一把跨进程的锁，本包提供它。
//
// 两条关键约定：
//   - 抢锁用 `SET key token NX PX ttl` **一条命令**完成。原子，且自带过期兜底：
//     持有者崩溃 / 被 kill -9 不会永久占锁，最多占到租期（ttl）结束。
//   - 释放用 Lua **先比较属主 token 再删**。避免「锁已过期、被别的实例抢走后，
//     原持有者仍执行 DEL」的误删——那会同时破坏两个实例的互斥。
//
// 语义边界：拿不到锁**不是错误**（TryLock 返回 ok=false 即可），由调用方决定是跳过本轮
// 还是稍后重试；Redis 访问失败才返回 err，调用方据此决定 fail-open 还是 fail-closed
// （本包不替调用方做这个判断，不同场景取向不同）。
package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// unlockLua 是「认属主再删」的释放脚本：只有键上的值仍等于本次持有的 token 才删。
//
// 若锁已过期并被别人抢走（值已变），此时删除会删掉**别人的锁**，等于把互斥一起删了，
// 因此必须比较后再决定。返回 1 表示确实由本次持有者删除；0 表示锁已不属于自己。
//
// KEYS[1] 锁的键
// ARGV[1] 本次持有的属主 token
const unlockLua = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

// Mutex 是一把具名分布式互斥锁。
//
// 一把 Mutex 绑定「一个键 + 一个租期」，可反复 TryLock / Unlock（每次 TryLock 生成新的
// 属主 token），因此同一个实例能覆盖「每小时一轮」这类周期性任务。
// 与 sync.Mutex 一样，它一次只被一个执行者持有；Unlock 必须由 TryLock 成功的那个
// goroutine 调用（token 记在实例上）。
type Mutex struct {
	rdb    *redis.Client
	key    string
	ttl    time.Duration
	script *redis.Script
	token  string // 最近一次 TryLock 成功时写入的属主 token；空表示未持有
}

// New 构造一把锁。ttl 是租期，须大于「单次持锁的最长耗时」。
func New(rdb *redis.Client, key string, ttl time.Duration) *Mutex {
	return &Mutex{
		rdb:    rdb,
		key:    key,
		ttl:    ttl,
		script: redis.NewScript(unlockLua),
	}
}

// TryLock 尝试立即获取锁，不等待。
//
// ok=false 表示锁正被其它实例持有（正常竞争结果，不算错误）；
// err != nil 表示 Redis 访问失败，调用方需自行决定 fail-open / fail-closed。
func (m *Mutex) TryLock(ctx context.Context) (ok bool, err error) {
	token, err := newToken()
	if err != nil {
		return false, fmt.Errorf("生成锁属主 token 失败: %w", err)
	}

	ok, err = m.rdb.SetNX(ctx, m.key, token, m.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("获取分布式锁失败: %w", err)
	}
	if ok {
		m.token = token
	}
	return ok, nil
}

// Unlock 释放锁。
//
// released=false 表示锁已不属于本次持有者（已过期或被别人抢走），此时不会删除它——
// 属于正常情况（如单轮耗时超过租期），不是错误。
func (m *Mutex) Unlock(ctx context.Context) (released bool, err error) {
	if m.token == "" {
		return false, errors.New("未持有锁，无法释放")
	}

	n, err := m.script.Run(ctx, m.rdb, []string{m.key}, m.token).Int64()
	if err != nil {
		return false, fmt.Errorf("释放分布式锁失败: %w", err)
	}
	m.token = ""
	return n == 1, nil
}

// newToken 生成锁的属主标识：128 位随机十六进制串。
//
// 必须是不可猜测的随机值而不是固定值 / 进程号：token 就是「谁持有锁」的唯一凭据，
// 可预测就会让别的实例伪造出删除他人锁的凭据。
func newToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
