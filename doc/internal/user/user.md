# user — 用户服务

代码：`internal/user/`（`models/`、`v1/{biz,repo,service}.go`），proto：`proto/user/v1/`，进程：`cmd/user`（端口 `:8083`）。

负责用户资料（昵称/简介/头像/metadata）、关注关系与封禁。uid 由 auth 服务分配后透传，本服务不管理凭证。

## 数据模型

`models.UserProfile`（表 `user_profiles`）：

| 字段 | 说明 |
| --- | --- |
| `BaseModel` | 雪花 ID、created_at、updated_at |
| `Name` / `Bio` | 昵称 `VARCHAR(64)` / 简介 `VARCHAR(512)`（rune 计数，`utils.ValidateName/ValidateBio` 校验） |
| `Avatar` | 嵌入 `media.MediaFile`（列前缀 `avatar_`）。**头像必填**，含 `avatar_id` / `avatar_asset_id` 等嵌入列 |
| `Status` | `UserStatus`（ACTIVE / BANNED） |
| `Followers` / `Followings` | 粉丝 / 关注数（关注操作事务内双端维护） |
| `MetaData` | `jsonb`，空值 ≡ `{}`（`utils.UnmarshalJSON`） |

`models.UserFollow`（表 `user_follows`）：一行 = Fan 关注 Following，防重复靠唯一约束。

## 接口（UserService）

| RPC | 说明 |
| --- | --- |
| `CreateUser` | 创建资料（uid 由 auth 分配后透传）；重复创建返回 `AlreadyExist`（`OnConflict DoNothing`） |
| `UpdateUserProfile` | 部分更新昵称/简介/metadata（`StringValue` 可空字段指示更新，nil 不修改） |
| `UpdateUserAvatar` | 独立头像更新；返回 `old_asset_id` 供 BFF 调整旧资产引用计数 |
| `GetUserBrief` / `GetUserProfile` / `GetUserStatus` | 批量简要 / 完整资料 / 状态查询 |
| `SetFollow` / `SetUnFollow` | 关注 / 取关（事务内双端计数维护；自关注拒绝，未关注取关幂等） |
| `GetFollowings` / `GetFollowingRelation` | 关注列表 / 关注关系查询 |
| `BanUser` | 封禁用户（状态流转） |

## 关键逻辑

### 头像必填
`CreateUser` / `UpdateUserAvatar` 拒绝无头像请求（`InvalidAvatar`）；`ToBrief` 对 nil 头像做防御。metadata 可选：未提供/空串 ≡ `{}`，仅非法 JSON 拒绝（`InvalidArgument`）。

### UpdateUserProfile 部分更新
可空字段（`google.protobuf.StringValue` / `JsonStr`）指示更新范围，nil 字段不修改。单事务内逐字段更新；PG 中存在的行即使值不变 UPDATE 也计 1 行，故任一更新 0 行即视为用户不存在，整体回滚返回 `NotFound`。空请求（无任何字段）→ `InvalidArgument`。

### UpdateUserAvatar 与旧资产
`old_asset_id` = 被替换头像所属媒体资产 ID，BFF 据此对旧资产减引用计数；user 服务只负责读旧值返回，不直接操作 media。

### 昵称/简介校验
`utils.ValidateName`（trim 非空、1~64 rune、禁控制字符）、`utils.ValidateBio`（≤512 rune、放行 `\n\r\t`），常量与列宽对齐（`NameMaxLen=64`/`BioMaxLen=512`）。

## 依赖

- `packages/utils` — 校验、JSON 解析（见 [packages/utils/utils.md](../../packages/utils/utils.md)）
- `UserRepository` — 数据访问，支持 `WithTX`（gorm gen 查询）
- `test/testdb`、`test/testmq` — 测试：真实 testdb（模板库克隆）+ MQ 模拟

## 状态

全部接口**已实现**并通过集成测试（`internal/user/v1/*_test.go`，真实 testdb + 真实 repo，仅 MQ 用 testmq 模拟）。
