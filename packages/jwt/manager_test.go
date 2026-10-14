package jwt

import (
	"strings"
	"testing"
	"time"

	authv1 "rednote/gen/go/auth/v1"
	"rednote/config"
	"rednote/packages/defaults"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signRaw 用指定密钥与算法签发裸 token（构造过期 / 篡改 / alg 攻击样本用）
func signRaw(t *testing.T, method jwtlib.SigningMethod, claims *Claims, secret string) string {
	t.Helper()
	signed, err := jwtlib.NewWithClaims(method, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

// ---------- NewManager ----------

func TestNewManager(t *testing.T) {
	t.Run("nil 配置 panic", func(t *testing.T) {
		assert.Panics(t, func() { NewManager(nil) }, "缺少配置必须立即失败")
	})

	t.Run("空密钥 panic", func(t *testing.T) {
		assert.Panics(t, func() {
			NewManager(&config.Config{JWT: config.JWTConfig{Secret: ""}})
		}, "缺少密钥必须立即失败")
	})

	t.Run("未配置过期时间时使用默认值", func(t *testing.T) {
		m := NewManager(&config.Config{JWT: config.JWTConfig{Secret: testSecret}})

		assert.Equal(t, defaults.TokenExpire, m.accessExpiry)
		assert.Equal(t, defaults.TokenExpire, m.refreshExpiry)
	})

	t.Run("非法过期时间回退默认值", func(t *testing.T) {
		m := NewManager(&config.Config{JWT: config.JWTConfig{
			Secret: testSecret, AccessExpire: -time.Hour, RefreshExpire: 0,
		}})

		assert.Equal(t, defaults.TokenExpire, m.accessExpiry)
		assert.Equal(t, defaults.TokenExpire, m.refreshExpiry)
	})

	t.Run("显式配置的过期时间被采用", func(t *testing.T) {
		m := NewManager(&config.Config{JWT: config.JWTConfig{
			Secret: testSecret, AccessExpire: time.Minute, RefreshExpire: 2 * time.Hour,
		}})

		assert.Equal(t, time.Minute, m.accessExpiry)
		assert.Equal(t, 2*time.Hour, m.refreshExpiry)
	})
}

// ---------- Generate ----------

func TestGenerate(t *testing.T) {
	t.Run("access token 签发成功并可解析回原载荷", func(t *testing.T) {
		// Arrange
		mgr := newTestManager(t)
		claims := &Claims{
			RegisteredClaims: jwtlib.RegisteredClaims{ID: "jti-1"},
			Type:             defaults.JwtAccessToken,
			Role:             authv1.Role_ROLE_ADMIN.String(),
			UID:              "12345",
		}

		// Act
		token, err := mgr.Generate(claims)

		// Assert
		require.NoError(t, err)
		assert.NotEmpty(t, token)

		got, err := mgr.Parse(token)
		require.NoError(t, err)
		assert.Equal(t, "12345", got.UID)
		assert.Equal(t, defaults.JwtAccessToken, got.Type)
		assert.Equal(t, authv1.Role_ROLE_ADMIN.String(), got.Role)
		assert.Equal(t, "jti-1", got.ID)
		assert.Equal(t, "test", got.Issuer, "未指定 issuer 时应补默认值")
		assert.NotNil(t, got.IssuedAt)
		assert.NotNil(t, got.ExpiresAt)
	})

	t.Run("refresh token 使用独立的过期时长", func(t *testing.T) {
		// Arrange
		mgr := NewManager(&config.Config{JWT: config.JWTConfig{
			Secret: testSecret, Issuer: "test", AccessExpire: time.Minute, RefreshExpire: 2 * time.Hour,
		}})
		claims := &Claims{Type: defaults.JwtRefreshToken, UID: "1"}

		// Act
		token, err := mgr.Generate(claims)
		require.NoError(t, err)

		// Assert
		got, err := mgr.Parse(token)
		require.NoError(t, err)
		require.NotNil(t, got.ExpiresAt)
		assert.WithinDuration(t, time.Now().Add(2*time.Hour), got.ExpiresAt.Time, 30*time.Second)
	})

	t.Run("已指定的 issuer 不被覆盖", func(t *testing.T) {
		mgr := newTestManager(t)
		claims := &Claims{
			RegisteredClaims: jwtlib.RegisteredClaims{Issuer: "custom"},
			Type:             defaults.JwtAccessToken,
			UID:              "1",
		}

		token, err := mgr.Generate(claims)
		require.NoError(t, err)

		got, err := mgr.Parse(token)
		require.NoError(t, err)
		assert.Equal(t, "custom", got.Issuer)
	})

	t.Run("未知 token 类型拒绝签发", func(t *testing.T) {
		mgr := newTestManager(t)

		token, err := mgr.Generate(&Claims{Type: "banana", UID: "1"})

		require.Error(t, err)
		assert.Empty(t, token)
	})

	t.Run("空 token 类型拒绝签发", func(t *testing.T) {
		mgr := newTestManager(t)

		token, err := mgr.Generate(&Claims{UID: "1"})

		require.Error(t, err)
		assert.Empty(t, token)
	})
}

// ---------- Parse ----------

func TestParse(t *testing.T) {
	t.Run("空串返回错误", func(t *testing.T) {
		mgr := newTestManager(t)

		claims, err := mgr.Parse("")

		require.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("非 token 串返回错误", func(t *testing.T) {
		mgr := newTestManager(t)

		claims, err := mgr.Parse("invalid.token.here")

		require.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("签名不符返回错误", func(t *testing.T) {
		// Arrange：用别的密钥签发
		mgr := newTestManager(t)
		bad := signRaw(t, jwtlib.SigningMethodHS256, &Claims{
			RegisteredClaims: jwtlib.RegisteredClaims{ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour))},
			Type:             defaults.JwtAccessToken,
			UID:              "1",
		}, "other_secret")

		// Act
		claims, err := mgr.Parse(bad)

		// Assert
		require.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("过期 token 返回错误", func(t *testing.T) {
		// Arrange
		mgr := newTestManager(t)
		expired := signRaw(t, jwtlib.SigningMethodHS256, &Claims{
			RegisteredClaims: jwtlib.RegisteredClaims{
				IssuedAt:  jwtlib.NewNumericDate(time.Now().Add(-2 * time.Hour)),
				ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(-time.Hour)),
			},
			Type: defaults.JwtAccessToken,
			UID:  "1",
		}, testSecret)

		// Act
		claims, err := mgr.Parse(expired)

		// Assert
		require.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("alg=none 伪造 token 被拒绝", func(t *testing.T) {
		// Arrange：无签名的 none 算法 token（经典 JWT 混淆攻击样本）
		mgr := newTestManager(t)
		forged, err := jwtlib.NewWithClaims(jwtlib.SigningMethodNone, &Claims{
			RegisteredClaims: jwtlib.RegisteredClaims{ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour))},
			Type:             defaults.JwtAccessToken,
			UID:              "1",
		}).SignedString(jwtlib.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)

		// Act
		claims, err := mgr.Parse(forged)

		// Assert
		require.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("载荷被篡改后签名失效", func(t *testing.T) {
		// Arrange：合法 token 的签名拼到另一份载荷上
		mgr := newTestManager(t)
		original := signRaw(t, jwtlib.SigningMethodHS256, &Claims{
			RegisteredClaims: jwtlib.RegisteredClaims{ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour))},
			Type:             defaults.JwtAccessToken,
			UID:              "1",
		}, testSecret)
		other := signRaw(t, jwtlib.SigningMethodHS256, &Claims{
			RegisteredClaims: jwtlib.RegisteredClaims{ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour))},
			Type:             defaults.JwtAccessToken,
			UID:              "attacker",
		}, testSecret)

		origParts := strings.Split(original, ".")
		otherParts := strings.Split(other, ".")
		require.Len(t, origParts, 3)
		tampered := origParts[0] + "." + otherParts[1] + "." + origParts[2]

		// Act
		claims, err := mgr.Parse(tampered)

		// Assert
		require.Error(t, err)
		assert.Nil(t, claims)
	})
}
