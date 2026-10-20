package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// refreshUsedKeyFmt 记录「某一枚刷新令牌已经被用过」，键里放的是令牌的 jti。
//
// 与前缀 `auth:code:` 并列，同样属于用户域的认证类状态。
const refreshUsedKeyFmt = "auth:rt:used:%s"

// RefreshStore 用 Redis 记录刷新令牌的使用状态，供重放检测使用。
//
// 刻意只放 Redis、不落库：这些键全部带 TTL（取令牌自身的剩余有效期），
// 令牌一过期记录就自动消失，既不需要清理任务，也不会留下永久增长的表。
type RefreshStore struct {
	rdb *redis.Client
}

// NewRefreshStore 构造 RefreshStore。
func NewRefreshStore(rdb *redis.Client) *RefreshStore {
	return &RefreshStore{rdb: rdb}
}

// MarkUsed 原子地标记某枚刷新令牌「已被轮换过」，并返回它此前是否已被标记。
//
// 用 SetNX 一次完成「判断 + 占用」：拆成先 EXISTS 再 SET 的话，同一枚令牌的
// 两个并发请求会双双判定为「没用过」而都放行，重放恰恰就是这么漏掉的。
//
// 返回 true 表示这枚令牌之前已经用过一次——调用方据此按「令牌已泄露」处理。
func (s *RefreshStore) MarkUsed(ctx context.Context, jti string, ttl time.Duration) (bool, error) {
	inserted, err := s.rdb.SetNX(ctx, fmt.Sprintf(refreshUsedKeyFmt, jti), "1", ttl).Result()
	if err != nil {
		return false, err
	}
	// SetNX 返回 false 说明键已存在，即这枚令牌之前就被用过了。
	return !inserted, nil
}
