package jwt

import (
	"context"
	"testing"

	authv1 "rednote/gen/go/auth/v1"
	"rednote/packages/defaults"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewClaim 载荷构造：类型/角色/UID 映射正确，jti 非空且每次唯一
func TestNewClaim(t *testing.T) {
	// Act
	claim := NewClaim(defaults.JwtAccessToken, authv1.Role_ROLE_ADMIN, 42)

	// Assert
	require.NotNil(t, claim)
	assert.Equal(t, defaults.JwtAccessToken, claim.Type)
	assert.Equal(t, authv1.Role_ROLE_ADMIN.String(), claim.Role)
	assert.Equal(t, "42", claim.UID)
	assert.NotEmpty(t, claim.ID, "jti 应自动生成")

	other := NewClaim(defaults.JwtRefreshToken, authv1.Role_ROLE_USER, 43)
	assert.NotEqual(t, claim.ID, other.ID, "每次签发的 jti 必须唯一")
	assert.Equal(t, defaults.JwtRefreshToken, other.Type)
}

// TestTokenContext 原始 token 的 ctx 存取
func TestTokenContext(t *testing.T) {
	t.Run("写入后可读回", func(t *testing.T) {
		ctx := WithToken(context.Background(), "tok-1")
		assert.Equal(t, "tok-1", TokenFromContext(ctx))
	})

	t.Run("空 token 不写入，读回空串", func(t *testing.T) {
		ctx := WithToken(context.Background(), "")
		assert.Equal(t, "", TokenFromContext(ctx))
	})

	t.Run("无 token 的 ctx 读回空串", func(t *testing.T) {
		assert.Equal(t, "", TokenFromContext(context.Background()))
	})
}

// TestClaimsContext 解析后 Claims 的 ctx 存取
func TestClaimsContext(t *testing.T) {
	t.Run("写入后可读回", func(t *testing.T) {
		claims := &Claims{Type: defaults.JwtAccessToken, UID: "1"}
		ctx := WithClaims(context.Background(), claims)

		got := ClaimsFromContext(ctx)
		require.NotNil(t, got)
		assert.Same(t, claims, got)
	})

	t.Run("nil claims 不写入，读回 nil", func(t *testing.T) {
		ctx := WithClaims(context.Background(), nil)
		assert.Nil(t, ClaimsFromContext(ctx))
	})

	t.Run("无 claims 的 ctx 读回 nil", func(t *testing.T) {
		assert.Nil(t, ClaimsFromContext(context.Background()))
	})

	t.Run("token 与 claims 相互独立", func(t *testing.T) {
		ctx := WithToken(context.Background(), "tok-2")

		assert.Nil(t, ClaimsFromContext(ctx))
		assert.Equal(t, "tok-2", TokenFromContext(ctx))
	})
}
