# auth — 认证服务

代码：`internal/auth/`（`models/`、`v1/{biz,repo,service}.go`），proto：`proto/auth/v1/`，进程：`cmd/auth`（端口 `:8081`）。

负责注册、密码登录、JWT 换发与登出。会话凭据由**内部 RPC 请求体直传**（`GetRefreshToken` / `GetAccessToken` 传 `refresh_token` 串，auth 自行验签；`Logout` 传 `uid`，由 BFF 从会话解析后注入），不再经 ctx/header 透传 claims。

## 数据模型

`models.UserCredential`（表 `user_credentials`）：

| 字段 | 说明 |
| --- | --- |
| `BaseModel` | 雪花 ID、created_at、updated_at |
| `Email` | 唯一索引 |
| `PasswordHash` | bcrypt 哈希 |
| `Role` | 角色（`authv1.Role`：USER / ADMIN） |
| `RefreshTokenIDWeb` / `RefreshTokenIDMobile` | 各平台当前有效 refresh token 的 `jti`，用于轮换与强制失效 |

## 接口（AuthService）

| RPC | 说明 |
| --- | --- |
| `Register` | 注册，返回 `assigned_id`（雪花 ID 字符串） |
| `Login` | 密码登录，签发 access + refresh token |
| `GetRefreshToken` | 无条件换发新 refresh token（请求体直传 refresh token 串，验签 + jti 轮换校验） |
| `GetAccessToken` | 无条件换发新 access token（同上，不轮换 refresh 登录态） |
| `Logout` | 强制登出（`uid` 由 BFF 从会话注入；失效两个平台的 refresh token） |

## 关键逻辑

### Register
1. 校验邮箱（RFC 5322 正则）与密码强度（8–32 位，含大小写字母与数字）。
2. `bcrypt` 哈希密码（`bcrypt.DefaultCost`）。
3. 创建 `UserCredential`；邮箱重复（`gorm.ErrDuplicatedKey`）返回 `DuplicateEmail`。
4. 返回 `assigned_id`（`credential.ID` 的雪花字符串形式），由上层透传给 post 服务建用户资料。

### Login
1. 邮箱/密码校验失败一律返回 `IncorrectCredential`（不泄露具体原因）。
2. `bcrypt.CompareHashAndPassword` 比对哈希。
3. 签发 refresh + access token（HS256）。
4. 按 `Platform`（MOBILE / WEB）把新 refresh token 的 `jti` 写入对应字段，作为该平台的"当前有效 token"。

### GetRefreshToken / GetAccessToken
- 会话凭据由请求体直传：`Manager.Parse(refresh_token)` 验签（伪造/过期/空串一律 `Unauthorized`），
  要求 token 类型为 **refresh**（`claim.Type == "refresh"`，否则 `InvalidArgument`）。
- `matchPlatform` 通过比对 `credential.RefreshTokenID*` 与 `claim.ID` 判断 token 是否仍有效（是否被轮换/登出）。
- `GetRefreshToken` 会轮换：生成新 `jti` 并覆盖对应平台字段（旧 refresh token 立即失效）。
- 未命中任何平台 = token 已失效，返回 `Unauthorized`。

### Logout
- 身份由请求体 `uid` 直传（BFF 从会话解析后注入，内网互信）；面客侧的越权防护由 BFF 会话鉴权保证。
- 在**同一事务**内把两个平台的 `RefreshTokenID*` 置为占位值（新生成的雪花 ID），立即失效。

## 依赖

- `jwt.Manager` — 签发/校验 token（见 [packages/jwt/jwt.md](../../packages/jwt/jwt.md)）
- `AuthRepository` — 数据访问，支持 `WithTX`
- ConnectRPC 拦截器：`trace.NewServerInterceptor()` + `validate.NewInterceptor()`（`cmd/auth/main.go` 组装）；
  不再挂 jwt 服务端拦截器——会话凭据由请求体直传，业务不读 ctx claims

## 状态

核心功能（注册/登录/换发/登出）**已实现**，含单元测试与 E2E 测试。
