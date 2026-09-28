package defaults

import "time"

// TokenExpire token 默认有效期，配置未指定或非法时使用
var TokenExpire = 24 * time.Hour

const (
	JwtAccessToken  = "access"
	JwtRefreshToken = "refresh"
)
