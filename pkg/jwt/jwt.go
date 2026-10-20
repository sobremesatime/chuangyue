// Package jwt 实现 JWT 双 Token（访问令牌 + 刷新令牌）的签发与校验。
//
// 双 Token 机制：
//   - 访问令牌（Access Token）有效期短（默认 15 分钟），随请求头 Authorization 携带；
//   - 刷新令牌（Refresh Token）有效期长（默认 7 天），存放在 HttpOnly Cookie 中；
//   - 访问令牌过期后，前端用刷新令牌换取新的访问令牌，无需重新登录。
//
// 两个 Token 使用不同密钥，即使访问令牌泄露也无法伪造刷新令牌。
package jwt

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
)

// Token 类型，写入 claims 防止两类令牌互相冒充。
const (
	TypeAccess  = "access"
	TypeRefresh = "refresh"
)

// jtiBytes 是令牌唯一标识的随机字节数（16 字节 → 32 位十六进制）。
const jtiBytes = 16

// Claims 是自定义载荷，除标准声明外携带用户 ID 与角色。
type Claims struct {
	UserID int64  `json:"uid"`
	Role   int8   `json:"role"`
	Type   string `json:"typ"`
	// TokenVersion 与 user.token_version 对应：重置密码时用户侧 +1，刷新时比对不上即失效。
	//
	// 存量令牌没有这个声明，反序列化得到 0，与用户表的默认值 0 一致，
	// 所以引入它不会把已在线的用户踢下线。
	TokenVersion int `json:"tv"`
	jwt.RegisteredClaims
}

// Manager 负责令牌的签发与解析，持有密钥与有效期配置。
type Manager struct {
	accessSecret  []byte
	refreshSecret []byte
	accessExpire  time.Duration
	refreshExpire time.Duration
	issuer        string
}

// NewManager 依据配置构造 Manager。
func NewManager(cfg config.JWTConfig) *Manager {
	return &Manager{
		accessSecret:  []byte(cfg.AccessSecret),
		refreshSecret: []byte(cfg.RefreshSecret),
		accessExpire:  cfg.AccessExpire,
		refreshExpire: cfg.RefreshExpire,
		issuer:        cfg.Issuer,
	}
}

// AccessExpire 返回访问令牌有效期，供上层计算 expires_in。
func (m *Manager) AccessExpire() time.Duration {
	return m.accessExpire
}

// RefreshExpire 返回刷新令牌有效期，供上层设置 Cookie Max-Age。
func (m *Manager) RefreshExpire() time.Duration {
	return m.refreshExpire
}

// GenerateAccessToken 签发访问令牌，返回令牌串与其有效秒数。
//
// tokenVersion 取自 user.token_version；重置密码后旧版本号的令牌会被拒绝续期。
func (m *Manager) GenerateAccessToken(userID int64, role int8, tokenVersion int) (string, int64, error) {
	token, err := m.sign(m.accessSecret, userID, role, tokenVersion, TypeAccess, m.accessExpire)
	if err != nil {
		return "", 0, err
	}
	return token, int64(m.accessExpire.Seconds()), nil
}

// GenerateRefreshToken 签发刷新令牌。
func (m *Manager) GenerateRefreshToken(userID int64, role int8, tokenVersion int) (string, error) {
	return m.sign(m.refreshSecret, userID, role, tokenVersion, TypeRefresh, m.refreshExpire)
}

// ParseAccessToken 校验访问令牌并返回载荷。
func (m *Manager) ParseAccessToken(tokenStr string) (*Claims, error) {
	return m.parse(tokenStr, m.accessSecret, TypeAccess)
}

// ParseRefreshToken 校验刷新令牌并返回载荷。
func (m *Manager) ParseRefreshToken(tokenStr string) (*Claims, error) {
	return m.parse(tokenStr, m.refreshSecret, TypeRefresh)
}

// sign 是签发令牌的公共逻辑。
//
// 每枚令牌都带一个随机 jti（RegisteredClaims.ID）：它是「这枚令牌是不是被用了第二次」
// 的唯一依据，刷新令牌的重放检测完全靠它（见 internal/user/service.Refresh）。
func (m *Manager) sign(secret []byte, userID int64, role int8, tokenVersion int, typ string, expire time.Duration) (string, error) {
	jti, err := newJTI()
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims := Claims{
		UserID:       userID,
		Role:         role,
		Type:         typ,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Issuer:    m.issuer,
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expire)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// newJTI 生成令牌唯一标识。
//
// 用 crypto/rand 而不是时间戳 / 计数器：jti 是重放检测的判据，
// 可预测或可碰撞的 id 会让「同一枚令牌被用第二次」这件事无法被识别出来。
func newJTI() (string, error) {
	buf := make([]byte, jtiBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成令牌 ID 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// parse 是校验令牌的公共逻辑，任何失败都归一为 errcode.ErrTokenInvalid。
func (m *Manager) parse(tokenStr string, secret []byte, wantType string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		// 只接受 HMAC 签名，防止算法混淆攻击（如改成 none）。
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("非预期的签名算法: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, errcode.ErrTokenInvalid
	}
	if !token.Valid {
		return nil, errcode.ErrTokenInvalid
	}
	if claims.Type != wantType {
		return nil, errcode.ErrTokenInvalid
	}
	return claims, nil
}

// IsTokenInvalid 判断错误是否属于令牌无效，便于上层区分处理。
func IsTokenInvalid(err error) bool {
	return errors.Is(err, errcode.ErrTokenInvalid)
}
