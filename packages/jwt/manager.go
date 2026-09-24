package jwt

import (
	"fmt"
	"time"

	"rednote/config"
	"rednote/packages/defaults"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

// Manager JWT 签发与校验
type Manager interface {
	// Generate 根据 Claims 签发 token
	Generate(claims *Claims) (string, error)
	// Parse 解析并校验 token，校验失败返回 error
	Parse(tokenString string) (*Claims, error)
}

// ManagerImpl 基于 HMAC-SHA256 的 JWT 实现
type ManagerImpl struct {
	secret []byte
	issuer string
	expire time.Duration
}

// NewManager 创建 JWT 管理器，配置从 config.JWTConfig 读取
func NewManager(cfg *config.Config) *ManagerImpl {
	if cfg == nil || cfg.JWT.Secret == "" {
		panic("jwt secret is required")
	}

	expire := cfg.JWT.Expire
	if expire <= 0 {
		expire = defaults.TokenExpire
	}

	return &ManagerImpl{
		secret: []byte(cfg.JWT.Secret),
		issuer: cfg.JWT.Issuer,
		expire: expire,
	}
}

// Generate 签发 HS256 token
// Claims 中未设置 issuer/iat/exp 时按配置补齐默认值
func (m *ManagerImpl) Generate(claims *Claims) (string, error) {
	now := time.Now()

	if claims.Issuer == "" && m.issuer != "" {
		claims.Issuer = m.issuer
	}
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwtlib.NewNumericDate(now)
	}
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwtlib.NewNumericDate(now.Add(m.expire))
	}

	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("jwt: 签发 token 失败: %w", err)
	}
	return signed, nil
}

// Parse 解析并校验 token（签名、alg、exp/nbf/iat）
func (m *ManagerImpl) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwtlib.ParseWithClaims(tokenString, claims, func(t *jwtlib.Token) (interface{}, error) {
		// 防止 alg 混淆攻击，只接受 HMAC 系列算法
		if _, ok := t.Method.(*jwtlib.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("jwt: 非预期的签名算法: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("jwt: 解析 token 失败: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("jwt: token 无效")
	}

	return claims, nil
}
