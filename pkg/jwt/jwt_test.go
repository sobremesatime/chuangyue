package jwt

import (
	"errors"
	"testing"
	"time"

	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
)

func newTestManager() *Manager {
	return NewManager(config.JWTConfig{
		AccessSecret:  "test-access-secret",
		RefreshSecret: "test-refresh-secret",
		AccessExpire:  time.Minute,
		RefreshExpire: time.Hour,
		Issuer:        "chuangyue-test",
	})
}

func TestAccessTokenRoundTrip(t *testing.T) {
	m := newTestManager()

	token, expiresIn, err := m.GenerateAccessToken(1001, 1, 3)
	if err != nil {
		t.Fatalf("签发访问令牌失败: %v", err)
	}
	if expiresIn != 60 {
		t.Errorf("expires_in 期望 60，实际 %d", expiresIn)
	}

	claims, err := m.ParseAccessToken(token)
	if err != nil {
		t.Fatalf("解析访问令牌失败: %v", err)
	}
	if claims.UserID != 1001 || claims.Role != 1 {
		t.Errorf("载荷不匹配: uid=%d role=%d", claims.UserID, claims.Role)
	}
	// 令牌版本要能原样带回来：重置密码的吊销判断完全依赖它。
	if claims.TokenVersion != 3 {
		t.Errorf("令牌版本不匹配: 期望 3，实际 %d", claims.TokenVersion)
	}
}

// TestTokenIDUnique 验证每枚令牌都带唯一 jti。
//
// 这是刷新令牌重放检测的前提：同一用户同一秒签发两枚令牌时，除 jti 之外的所有字段
// 完全相同（IssuedAt 精确到秒），一旦 jti 碰撞，「同一枚被用了第二次」就识别不出来。
func TestTokenIDUnique(t *testing.T) {
	m := newTestManager()

	tokenA, _, err := m.GenerateAccessToken(1001, 0, 0)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	tokenB, _, err := m.GenerateAccessToken(1001, 0, 0)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}

	claimsA, err := m.ParseAccessToken(tokenA)
	if err != nil {
		t.Fatalf("解析令牌失败: %v", err)
	}
	claimsB, err := m.ParseAccessToken(tokenB)
	if err != nil {
		t.Fatalf("解析令牌失败: %v", err)
	}
	if claimsA.ID == "" {
		t.Fatal("jti 不应为空")
	}
	if claimsA.ID == claimsB.ID {
		t.Fatalf("两枚令牌的 jti 不应相同: %s", claimsA.ID)
	}
}

func TestRefreshTokenCannotBeUsedAsAccess(t *testing.T) {
	m := newTestManager()

	refreshToken, err := m.GenerateRefreshToken(1001, 0, 0)
	if err != nil {
		t.Fatalf("签发刷新令牌失败: %v", err)
	}

	if _, err := m.ParseAccessToken(refreshToken); !errors.Is(err, errcode.ErrTokenInvalid) {
		t.Errorf("刷新令牌不应通过访问令牌校验，实际 err=%v", err)
	}
}

func TestTamperedTokenRejected(t *testing.T) {
	m := newTestManager()

	token, _, err := m.GenerateAccessToken(1001, 0, 0)
	if err != nil {
		t.Fatalf("签发访问令牌失败: %v", err)
	}

	if _, err := m.ParseAccessToken(token + "tampered"); !errors.Is(err, errcode.ErrTokenInvalid) {
		t.Errorf("被篡改的令牌应被拒绝，实际 err=%v", err)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	m := NewManager(config.JWTConfig{
		AccessSecret:  "test-access-secret",
		RefreshSecret: "test-refresh-secret",
		AccessExpire:  -time.Minute, // 立即过期
		RefreshExpire: time.Hour,
		Issuer:        "chuangyue-test",
	})

	token, _, err := m.GenerateAccessToken(1001, 0, 0)
	if err != nil {
		t.Fatalf("签发访问令牌失败: %v", err)
	}

	if _, err := m.ParseAccessToken(token); !errors.Is(err, errcode.ErrTokenInvalid) {
		t.Errorf("过期令牌应被拒绝，实际 err=%v", err)
	}
}
