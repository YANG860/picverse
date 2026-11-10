# post — 帖子服务

代码：`internal/post/`（`models/`、`v1/{biz,repo,service}.go`），proto：`proto/post/v1/`，进程：`cmd/post`（端口 `:8082`）。

负责草稿创作、发布审核流转、帖子查询与管理。uid 由 auth 服务分配后透传，本服务不管理凭证；帖子媒体来自 media 服务产出的 `MediaFile`（响应完整下发供 BFF 管道使用；**面客由 BFF 降维为 `MediaView`**，存储坐标不面客，见 [bff.md](../bff/bff.md)）。

## 数据模型

`models.PostDetail`（表 `posts`，草稿与已发布同表，`status` 区分）：

| 字段 | 说明 |
| --- | --- |
| `BaseModel` | 雪花 ID、created_at、updated_at |
| `AuthorID` | 作者 |
| `Title` / `FullText` | 标题 ≤100 rune / 正文 ≤1000 rune（`utils.ValidateTitle/ValidateContent` 校验） |
| `Status` | `PostStatus`（DRAFT / AUDITING / PUBLISHED / BANNED / DELETED，DELETED 为软删） |
| `LikeCount` 等 | 统计列（点赞/收藏/评论/浏览，`SyncPostStat` 逐字段覆盖：nil 不修改，设置则覆盖该列） |
| `MetaData` | `jsonb`，空值 ≡ `{}`（`utils.UnmarshalJSON`） |
| `Medias` | 关联 `PostMedia`，按 `sort_order` 加载 |

`models.PostMedia`（表 `post_medias`）：帖子↔媒体关联，嵌入 `media.MediaFile`（列前缀 `media_`），`SortOrder` 为展示顺序，**首张即封面**。

## 接口（PostService，19 rpc）

| RPC | 状态 | 说明 |
| --- | --- | --- |
| `CreateDraft` | ✅ | 创建草稿；媒体列表必填（≥ 1 张，首张即封面） |
| `UpdateDraft` | ✅ | 文本字段部分更新（title/full_text/metadata，nil 字段不修改） |
| `UpdateDraftMedias` | ✅ | 媒体全量替换（≥ 1 张，不支持清空）；返回 `old_asset_ids` |
| `PublishDraft` | ✅ | 发布草稿：DRAFT→AUDITING 并发审核消息（走 outbox） |
| `GetPostsByIDs` | ✅ | 批量获取（内部接口，BFF 内网调用；本身就无鉴权/可见性过滤，返回任意状态的帖子；`PostResult` 逐项 `fail`/`err`） |
| `GetPostStatus` | ✅ | 帖子状态查询 |
| `ViewPost` | ✅ | 查看帖子（非发布状态仅作者可见，软删对外隐藏，封禁返回 `Banned`） |
| `ViewPostByAuthor` | ✅ | 按作者游标分页（空页返回空列表 + 空游标，不以 NotFound 表达翻页到底）；`PostBrief` 携带 `status`，供 BFF 直接组装 `PostCard` 无需二次查询 |
| `DeletePost` | ✅ | 用户删帖（软删 → DELETED，事务内发 `DeletePost` 消息，consumer 物理回收） |
| `BanPost` | ✅ | 封禁帖子（管理侧，无 uid 校验；软封禁 → BANNED + outbox 发 `BanPost` 消息） |
| `SyncPostStat` | ✅ | 统计列同步（异步链路；逐字段部分更新，nil 不修改，设置则覆盖该列（含 0），仅统计不改状态） |
| `LikePost` / `UnlikePost` | ✅ | 点赞/取消（幂等；仅 PUBLISHED，非 PUBLISHED 一律 `NotFound`）；明细为准 + 同事务条件计数，响应 `{like_count, liked}` |
| `FavouritePost` / `UnfavouritePost` | ✅ | 收藏/取消（幂等；新增收藏校验 `FavouriteCap`，超限 `FavouriteLimitExceeded(1306)` 整体回滚）；响应 `{favourite_count, favourited}` |
| `ListLikedPostIDs` | ✅ | 最近点赞的帖子 ID（内部接口，BFF 会话注入 uid 转发；前端预取判态，`created_at DESC` 时间游标） |
| `ListFavouritePostIDs` | ✅ | 收藏的帖子 ID（同构；`FavouriteCap` 内一页可拉全） |

## 关键逻辑

### 封面规范（所有帖子至少有一个封面图）
媒体集合的唯一写入口是 `CreateDraft` 与 `UpdateDraftMedias`，两者都强制媒体 ≥ 1 张（`InvalidMedia`；proto 层 `repeated.min_items = 1`），首张媒体即封面，故不变式恒成立。`UpdateDraftMedias` 为全量替换，不支持清空。

### 草稿部分更新
`UpdateDraft` 只更新文本字段：可空字段（`StringValue` / `JsonStr`）指示更新范围，nil 不修改；metadata 空值 ≡ `{}`，仅非法 JSON 拒绝（`InvalidArgument`）；空请求（无任何字段）→ `InvalidArgument`。媒体更新独立于 `UpdateDraftMedias`。

### 更新并发控制与错误
草稿更新走单事务（`Repo.Transaction`）：先 `SELECT ... FOR UPDATE` 原子断言"存在且仍为 DRAFT"并持有 `posts` 行锁（避免与 `PublishDraft`/`DeletePost` 的并发状态流转竞态），再校验 uid==作者、逐字段更新、同事务回读完整帖子。注意聚合查询不能携带 `FOR UPDATE`（PG `SQLSTATE 0A000`），加锁须用非聚合查询。

更新失败的错误码**三态区分**（与实现一致）：不存在 → `NotFound`（1002）；非作者 → `Forbidden`（1005）；非草稿/已流转（含已软删）→ `NotEditable`（1304，内部不细分具体状态）。

### UpdateDraftMedias 与旧资产
`old_asset_ids` = 被替换媒体所属**资产 ID**（`MediaFile.AssetID`），BFF 据此对旧资产调整引用计数；post 服务只读旧值返回，不直接操作 media（同 [user.md](../user/user.md) 的 `old_asset_id`）。首次设置媒体时为空。

媒体文件的**存在性/归属校验与资产引用计数统一由 BFF 维护**：两个写入口（`CreateDraft`/`UpdateDraftMedias`）只存关联，不做归属校验（`utils.ValidateMedia` 仅基础格式校验）。

### 发布链路
`PublishDraft` 校验 uid==作者后，事务内 `SetPostStatus(DRAFT→AUDITING)` 并经 outbox 写 `AuditPost` 消息（见 [common.md](../common/common.md)），poller 投递 JetStream；consumer 审核通过后 `AUDITING→PUBLISHED` 并发发布消息（`internal/consumer/post_consumer.go`）。

当前审核为**占位实现**：`PublishDraft` 不校验“图片审核通过”、consumer `handleAuditPost` 无实质审核逻辑（TODO 保留），审核完直接流转 `PUBLISHED`；待审核链路完善后补。

### 删帖/封禁消息链路（consumer）
`DeletePost` 软删（事务内发 `DeletePost` 消息）→ consumer `handleDeletePost` 同事务**物理回收**软删（DELETED 状态）帖子行与其媒体关联，重复投递幂等；`repo.DeletePost` 带 `status=DELETED` 守卫，不会误删未删帖的裸行。**资产计数回收**在同链路闭环：`DeletePostMedias` 先查后删返回被删关联的资产 ID，按槽位同事务写 outbox `deref_media` 消息（media consumer 递减引用计数），删几槽减几槽，重复投递时 0 行命中即不重复减。`BanPost`（软封禁）与发布后的 `PublishPost` 消息目前由 consumer 占位消费（日志留痕 + ACK，对齐 media 侧 `HandlePublishMedia` 模式），下游逻辑（媒体资产处置/通知等）待需求明确后补。

### 互动（点赞/收藏/浏览计数）
- **强一致计数**：明细为准（`post_likes`/`post_favourites` 唯一约束 `(post_id, user_id)` + `OnConflict DoNothing` 幂等锚点），明细增删与 `posts.like_count`/`favourite_count` 条件更新同事务；减法为防负条件更新（仅计数 > 0 才减）。
- **幂等口径**：重复点赞/收藏返回成功且不重复计数——前端可重复动作收敛（互动态前端预取判态的容错基础）。
- **状态裁决**：仅 PUBLISHED 可互动；非 PUBLISHED/不存在一律 `NotFound`，不泄露帖子状态。
- **收藏夹容量**：`defaults.FavouriteCap`（1000）；仅新增收藏校验（同事务计数含自身，超限回滚插入）；已藏重复调用幂等成功不报满。
- **浏览计数 PV 近似（Redis 批量写回）**：BFF `GetPost` 他人访问时经 `PostEntityCache.IncrPostViewCount` 计数 +1（作者自访不计，失败仅 Warn 不打挂详情）；`internal/poller.RedisPoller`（cmd/poller 常驻）定时（`config.post.view_flush_interval`，默认 30s）原子取走（GETDEL）增量写回 `posts.view_count`。只丢不重：取走在前写库在后，失败丢该批增量；不建 view 明细表，`view_count` 为近似值（计数器约定见 [packages/cache/cache.md](../../packages/cache/cache.md)）。
- **判态列表**：`ListLikedPostIDs`/`ListFavouritePostIDs` 供前端预取 post_ids 本地判态（赞态近似、藏态精确）；时间游标严格小于边界，同毫秒并列行在页边界可能落页外（与现有列表翻页同口径）。

## 依赖

- `packages/utils` — 校验、JSON 解析（见 [packages/utils/utils.md](../../packages/utils/utils.md)）
- `packages/errmsg` — 可枚举业务错误（见 [packages/errmsg/err.md](../../packages/errmsg/err.md)）
- `PostRepository` — 数据访问，支持 `WithTX` / `Transaction`（gorm gen 查询）
- `test/testdb`、`test/testmq` — 测试：真实 testdb（模板库克隆）+ MQ 模拟

## 状态

`PostService` 的 17 个 rpc **全部已实现**：草稿链路（`CreateDraft` / `UpdateDraft` / `UpdateDraftMedias` / `PublishDraft`）、查询（`GetPostsByIDs` / `GetPostStatus` / `ViewPost` / `ViewPostByAuthor`）、管理（`DeletePost` / `BanPost` / `SyncPostStat`）、互动（`LikePost` / `UnlikePost` / `FavouritePost` / `UnfavouritePost` / `ListLikedPostIDs` / `ListFavouritePostIDs`）。

测试：`internal/post/v1` 含 repo 层与 biz 层集成测试（`repo_test.go` / `biz_test.go`，真实 testdb 模板库克隆 + testmq 内存模拟，覆盖正常/边界/错误路径与状态机），依赖 `test/testdb`。
