// 本文件封装邮箱验证码在 Redis 中的存取。
//
// 验证码的「读 + 比对 + 计数 + 删除」必须是原子操作：拆成多条命令的话，
// 攻击者可以并发提交大量请求绕过错误次数上限，因此这里全部用 Lua 脚本实现。
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sobremesatime/chuangyue/pkg/errcode"
)

// 验证码相关的 Redis key 格式。统一加 auth: 前缀，避免污染业务缓存。
const (
	codeKeyFmt      = "auth:code:%s:%s"     // purpose, email
	tryKeyFmt       = "auth:code:try:%s:%s" // purpose, email
	cooldownKeyFmt  = "auth:code:cd:%s:%s"  // purpose, email
	emailHourKeyFmt = "auth:code:em:%s:%s"  // email, yyyyMMddHH
	ipHourKeyFmt    = "auth:code:ip:%s:%s"  // ip, yyyyMMddHH
)

// hourBucketLayout 是小时级限流的分桶键格式。
const hourBucketLayout = "2006010215"

// hourTTL 是小时计数 key 的存活时间，略大于 1 小时即可。
const hourTTL = time.Hour

// Lua 脚本返回码。
const (
	luaOK         = "OK"
	luaCooldown   = "COOLDOWN"
	luaEmailLimit = "EMAIL_LIMIT"
	luaIPLimit    = "IP_LIMIT"
	luaNotFound   = "NOTFOUND"
	luaMismatch   = "MISMATCH"
	luaTooMany    = "TOOMANY"
)

// issueScript 一次往返完成「冷却判断 + 双重限流 + 写入验证码」。
//
//	KEYS: 1=code 2=try 3=cooldown 4=emailHour 5=ipHour
//	ARGV: 1=code 2=ttlSec 3=cooldownSec 4=emailLimit 5=ipLimit 6=hourTtlSec
var issueScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[3]) == 1 then return 'COOLDOWN' end
if tonumber(redis.call('GET', KEYS[4]) or '0') >= tonumber(ARGV[4]) then return 'EMAIL_LIMIT' end
if tonumber(redis.call('GET', KEYS[5]) or '0') >= tonumber(ARGV[5]) then return 'IP_LIMIT' end

redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
redis.call('DEL', KEYS[2])
redis.call('SET', KEYS[3], '1', 'EX', ARGV[3])

local em = redis.call('INCR', KEYS[4])
if em == 1 then redis.call('EXPIRE', KEYS[4], ARGV[6]) end
local ip = redis.call('INCR', KEYS[5])
if ip == 1 then redis.call('EXPIRE', KEYS[5], ARGV[6]) end
return 'OK'
`)

// verifyScript 原子完成「读取 + 比对 + 计错 + 删除」。
//
//	KEYS: 1=code 2=try
//	ARGV: 1=input 2=maxTry 3=ttlSec
var verifyScript = redis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored then return 'NOTFOUND' end
if tonumber(redis.call('GET', KEYS[2]) or '0') >= tonumber(ARGV[2]) then return 'TOOMANY' end
if stored ~= ARGV[1] then
  redis.call('INCR', KEYS[2])
  redis.call('EXPIRE', KEYS[2], ARGV[3])
  return 'MISMATCH'
end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
return 'OK'
`)

// CodePolicy 描述一次发码/校验使用的策略参数，由配置注入。
type CodePolicy struct {
	TTL          time.Duration // 验证码有效期
	Cooldown     time.Duration // 同一邮箱两次发码的最小间隔
	MaxTry       int           // 单个验证码允许的错误次数
	EmailHourCap int           // 单邮箱每小时发码上限
	IPHourCap    int           // 单 IP 每小时发码上限
}

// CodeStore 是邮箱验证码的 Redis 存取器。
type CodeStore struct {
	rdb *redis.Client
}

// NewCodeStore 构造 CodeStore。
func NewCodeStore(rdb *redis.Client) *CodeStore {
	return &CodeStore{rdb: rdb}
}

// Issue 写入验证码，并完成冷却与双重限流判断。
//
// 限流一律返回 errcode.ErrTooManyReq：只有该码会被响应层映射成 HTTP 429，
// 新增 2xxxx 码会因为 httpStatus 的 default 分支而返回 200，前端拿不到限流语义。
func (s *CodeStore) Issue(ctx context.Context, purpose, email, ip, code string, p CodePolicy) error {
	keys := []string{
		fmt.Sprintf(codeKeyFmt, purpose, email),
		fmt.Sprintf(tryKeyFmt, purpose, email),
		fmt.Sprintf(cooldownKeyFmt, purpose, email),
		fmt.Sprintf(emailHourKeyFmt, email, time.Now().Format(hourBucketLayout)),
		fmt.Sprintf(ipHourKeyFmt, ip, time.Now().Format(hourBucketLayout)),
	}
	args := []any{
		code,
		int(p.TTL.Seconds()),
		int(p.Cooldown.Seconds()),
		p.EmailHourCap,
		p.IPHourCap,
		int(hourTTL.Seconds()),
	}

	res, err := issueScript.Run(ctx, s.rdb, keys, args...).Text()
	if err != nil {
		return err
	}

	switch res {
	case luaOK:
		return nil
	case luaCooldown:
		return errcode.ErrTooManyReq.WithMsg("验证码已发送，请稍后再试")
	case luaEmailLimit:
		return errcode.ErrTooManyReq.WithMsg("该邮箱获取验证码过于频繁，请稍后再试")
	case luaIPLimit:
		return errcode.ErrTooManyReq.WithMsg("当前网络获取验证码过于频繁，请稍后再试")
	default:
		return fmt.Errorf("未知的发码脚本返回: %s", res)
	}
}

// Verify 校验验证码，成功后立即销毁。
//
// 冷却锁不在这里清除：校验成功不代表可以马上重新发码，
// 否则「收发验证码」就变成可以无限刷的接口。
func (s *CodeStore) Verify(ctx context.Context, purpose, email, code string, p CodePolicy) error {
	keys := []string{
		fmt.Sprintf(codeKeyFmt, purpose, email),
		fmt.Sprintf(tryKeyFmt, purpose, email),
	}
	res, err := verifyScript.Run(ctx, s.rdb, keys, code, p.MaxTry, int(p.TTL.Seconds())).Text()
	if err != nil {
		return err
	}

	switch res {
	case luaOK:
		return nil
	case luaTooMany:
		return errcode.ErrCodeTooManyAttempts
	case luaNotFound, luaMismatch:
		// 「从未发送」与「已过期」对用户是同一件事，统一返回一个提示。
		return errcode.ErrCodeInvalid
	default:
		return fmt.Errorf("未知的校验脚本返回: %s", res)
	}
}

// Release 在邮件投递失败时释放验证码与冷却锁。
//
// 小时计数不回退：投递失败也可能是被滥用导致，保留计数更安全。
func (s *CodeStore) Release(ctx context.Context, purpose, email string) error {
	return s.rdb.Del(ctx,
		fmt.Sprintf(codeKeyFmt, purpose, email),
		fmt.Sprintf(cooldownKeyFmt, purpose, email),
	).Err()
}
