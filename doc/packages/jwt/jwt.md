# jwt — JWT 签发 / 校验

代码：`packages/jwt/`（`jwt.go`、`manager.go`、`interceptor.go`）。

提供 JWT 的签发、校验、Claims 传递，以及 ConnectRPC 的认证拦截器。签名算法固定 **HS256**（HMAC-SHA256）。

## Claims

`jwt.Claims` 内嵌 `jwtlib.RegisteredClaims`，并扩展：

| 字段 | 说明 |
| --- | --- |
| `Type` | token 类型：`"access"` / `"refresh"`（`packages/defaults`） |
| `Role` | 角色字符串 |
| `UID` | 用户雪花 ID 字符串 |
| `RegisteredClaims.ID` | token 的 `jti`（唯一 ID），用于轮换/失效 |

`NewClaim(tokenType, role, uid)` 生成新 Claims（`jti` 用雪花 ID）。

## context 传递

| 函数 | 说明 |
| --- | --- |
| `WithToken` / `TokenFromContext` | 附加 / 读取原始 token 字符串 |
| `WithClaims` / `ClaimsFromContext` | 附加 / 读取解析后的 `*Claims` |

## Manager

`jwt.Manager` 接口：`Generate(claims) (string, error)`、`Parse(tokenString) (*Claims, error)`。实现 `ManagerImpl`：

- `NewManager(cfg)` — 读取 `config.JWT`（`secret` 必填，缺失 panic；有效期缺失回退 `defaults.TokenExpire` 24h）。
- `Generate` — 按 `claims.Type` 设定过期时间（access 用 `accessExpiry`、refresh 用 `refreshExpiry`），补 `issuer`/`iat`，HS256 签名。
- `Parse` — 解析并校验（签名、alg、exp/nbf/iat），**只接受 HMAC 系列算法**防 `alg` 混淆攻击。

## 拦截器

### ServerInterceptor（`NewServerInterceptor(m)`）
- 从 `Authorization: Bearer <token>` 提取并解析 token。
- 成功：把原始 token + Claims 注入 ctx，供业务读取。
- 无 token / 解析失败：跳过注入，**不拦截**——是否强制认证由业务层决定。

### ClientInterceptor（`NewClientInterceptor()`）
- 发送请求时若 ctx 有原始 token，以 `Bearer` 方案写入 `Authorization` 头。

token 传输约定：标准 `Authorization` 头 + `Bearer` 认证方案。auth 服务在 `cmd/auth/main.go` 组装服务端拦截器。
