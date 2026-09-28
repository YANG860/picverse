package jwt

import (
	"context"
	authv1 "rednote/gen/go/auth/v1"
	"rednote/packages/utils"

	"github.com/bwmarrin/snowflake"
	jwtlib "github.com/golang-jwt/jwt/v5"
)

type (
	tokenKey  struct{}
	claimsKey struct{}
)

// Claims JWT 载荷（payload）结构
type Claims struct {
	jwtlib.RegisteredClaims

	Type string `json:"type"`
	Role string `json:"role"`
	UID  string `json:"uid"`
}

func NewClaim(tokenType string, role authv1.Role, uid int64) *Claims {
	return &Claims{
		RegisteredClaims: jwtlib.RegisteredClaims{
			ID: utils.GenID().String(),
		},
		Type: tokenType,
		Role: role.String(),
		UID:  snowflake.ParseInt64(uid).String(),
	}
}

// WithToken 把原始 token 附加到 ctx
func WithToken(ctx context.Context, token string) context.Context {
	if token != "" {
		return context.WithValue(ctx, tokenKey{}, token)
	}
	return ctx
}

// TokenFromContext 从 ctx 取原始 token，不存在返回空串
func TokenFromContext(ctx context.Context) string {
	if token, ok := ctx.Value(tokenKey{}).(string); ok {
		return token
	}
	return ""
}

// WithClaims 把解析后的 Claims 附加到 ctx
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	if claims != nil {
		return context.WithValue(ctx, claimsKey{}, claims)
	}
	return ctx
}

// ClaimsFromContext 从 ctx 取解析后的 Claims，不存在返回 nil
func ClaimsFromContext(ctx context.Context) *Claims {
	if claims, ok := ctx.Value(claimsKey{}).(*Claims); ok {
		return claims
	}
	return nil
}
