# 数据库建表与索引设计报告

## 文件说明

| 文件 | 用途 |
|------|------|
| `001_create_media_tables.sql` | media 建表 DDL（media_assets、media_files）及索引 |
| `002_create_outbox_tables.sql` | outbox 建表 DDL（out_box_entries）及索引（含部分索引） |
| `003_create_auth_tables.sql` | auth 建表 DDL（user_credentials）及索引 |
| `004_create_post_tables.sql` | post 建表 DDL（posts、post_medias）及索引 |
| `005_create_user_tables.sql` | user 建表 DDL（user_profiles、user_follows）及索引 |
| `006_create_consumer_dedup_tables.sql` | 消费端幂等去重表（processed_messages）建表 DDL |
| `007_create_post_interaction_tables.sql` | post 互动表（post_likes、post_favourites）建表 DDL 及约束 |
| `009_create_fanout_tables.sql` | 关注流扇出收件箱（fanout_timelines）建表 DDL 及索引 |

> **文件组织**：按模块组织，一个模块一个文件 = 建表 + 索引。
> 所有脚本幂等（`IF NOT EXISTS`），按文件名序执行，由 `scripts/dbsync`、`scripts/dbinit`、
> docker-entrypoint-initdb.d（首次建库）三处统一消费 `sql/*.sql`。

---

## 数据模型总览

共 **11 张表**，时间字段为 unix 毫秒；除 `fanout_timelines`（(uid, post_id) 复合主键，幂等扇出）外均使用雪花 ID 作为主键：

| 表名 | 用途 | 模型来源 |
| ------ | ------ | ---------- |
| `media_assets` | 媒体资产（一个原图 + 所有变体的抽象） | `internal/media/models/models.go` |
| `media_files` | 媒体文件（原图/216px/540px/1080px 具体变体） | `internal/media/models/models.go` |
| `out_box_entries` | Outbox 消息发件箱（事务性可靠消息投递） | `internal/common/models/outbox.go` |
| `user_credentials` | 用户凭证（邮箱、密码哈希、角色、刷新令牌） | `internal/auth/models/models.go` |
| `user_profiles` | 用户资料（昵称、简介、统计、头像嵌入列） | `internal/user/models/models.go` |
| `user_follows` | 用户关注关系（一行 = Fan 关注 Following） | `internal/user/models/models.go` |
| `posts` | 帖子（草稿与已发布同表，status 区分） | `internal/post/models/models.go` |
| `post_medias` | 帖子与媒体关联 | `internal/post/models/models.go` |
| `processed_messages` | 消费端幂等去重记录（AtLeastOnce 恰好一次效果） | `internal/common/models/dedup.go` |
| `post_likes` | 帖子点赞明细（一行 = 一赞，行数即 like_count） | `internal/post/models/models.go` |
| `post_favourites` | 帖子收藏明细（一行 = 一藏） | `internal/post/models/models.go` |
| `fanout_timelines` | 关注流扇出收件箱（推拉结合 push 路径，复合主键） | `internal/discover/models/models.go` |

---

## 索引设计与依据

### media_assets（3 个索引，`001_create_media_tables.sql`）

#### 1. `idx_media_assets_created_at` — 时间分页查询

```sql
ON media_assets (created_at DESC)
```

**对应代码**：`repo.ListMediaAssetsBefore(ctx, t, limit)`

```go
WHERE created_at < ? ORDER BY created_at DESC LIMIT ?
```

**依据**：游标分页是核心查询模式，需要按 `created_at` 范围过滤 + 降序排序。B-tree 索引同时覆盖过滤和排序，避免全表扫描和 filesort。

---

#### 2. `idx_media_assets_status_refcount_created` — 未使用媒体清理

```sql
ON media_assets (status, ref_count, created_at)
```

**对应代码**：`repo.GetUnusedMedia(ctx, limit)`

```go
WHERE (ref_count <= 0 AND status = 4 AND created_at < ?)   -- FAILED
   OR (ref_count <= 0 AND status = 3 AND created_at < ?)   -- ACTIVE
LIMIT ?
```

**依据**：这是定时清理任务的查询，三列组合可精确覆盖两个 OR 分支。`status` 放首位（等值过滤，基数最低），`ref_count` 次之（范围），`created_at` 最后（时间阈值过滤）。考虑过拆成两个部分索引，但 OR 条件合并为一个复合索引更简洁。

---

#### 3. `idx_media_assets_uploader_id` — 按上传者查询

```sql
ON media_assets (uploader_id)
```

**对应代码**：模型定义 `gorm:"index"` tag

**依据**：当前代码中 `uploader_id` 已声明为索引列。虽然当前无直接查询使用，但作为用户维度的查询入口（如"查看我的上传"），保留此索引以支持未来扩展。

---

### media_files（1 个索引，`001_create_media_tables.sql`）

#### 4. `idx_media_files_asset_type` — 资产文件联合查询

```sql
ON media_files (asset_id, type)
```

**对应代码**：`repo.GetMediaFiles(ctx, ids, types)`

```go
WHERE (id = ? AND type = ?) OR (id = ? AND type = ?) ...
```

**依据**：每次查询一组 `(asset_id, type)` 对，复合索引可精确匹配。模型中已通过 `index:idx_media_files_asset_type` tag 声明。`AssetID` 在前（高基数、等值匹配），`Type` 在后（低基数、等值匹配）。

---

### out_box_entries（4 个索引，`002_create_outbox_tables.sql`）

#### 5. `idx_outbox_pending_poll` — Poller 轮询热路径 ⭐ 最关键

```sql
ON out_box_entries (status, retry_count, send_at)
WHERE status = 0;  -- 部分索引
```

**对应代码**：`poller.Work()`

```go
WHERE send_at < now()
  AND status = STATUS_PENDING    -- 0
  AND retry_count < maxRetry
FOR UPDATE SKIP LOCKED
LIMIT batchSize
```

**依据**：这是整个系统最关键的查询——Poller 每隔 `interval` 执行一次，直接决定消息投递延迟。

- **部分索引** `WHERE status = 0`：已完成/失败的消息占绝大多数，部分索引仅索引待处理行，索引体积从全表缩小到活跃行
- 列顺序 `status → retry_count → send_at`：`status` 已通过部分索引过滤；`retry_count` 为范围过滤（`< maxRetry`）；`send_at` 为范围过滤 + 隐式排序方向

---

#### 6. `idx_outbox_send_at` — 按发送时间查询

```sql
ON out_box_entries (send_at)
```

**依据**：`send_at` 用于延迟消息调度，除 Poller 热路径外，也可能用于运维排查、监控面板等场景。单独索引支持通用的时间范围查询。

---

#### 7. `idx_outbox_trace_id` — 链路追踪查询

```sql
ON out_box_entries (trace_id)
```

**对应代码**：模型定义 `gorm:"index"` tag

**依据**：支持通过 trace_id 追踪一条消息从创建到投递的全链路，是排查问题的核心查询入口。

---

#### 8. `idx_outbox_subject` — 按主题查询

```sql
ON out_box_entries (subject)
```

**对应代码**：模型定义 `gorm:"index"` tag

**依据**：支持按 NATS 主题过滤消息（如查看特定消息类型的积压情况），运维和监控场景需要。

---

### user_credentials（1 个索引，`003_create_auth_tables.sql`）

#### 9. `idx_user_credentials_email` — 邮箱唯一登录

```sql
ON user_credentials (email)  -- UNIQUE
```

**对应代码**：模型定义 `gorm:"uniqueIndex"` tag（`internal/auth/models/models.go`）

```sql
WHERE email = ?   -- 注册查重 / 登录取凭证
```

**依据**：邮箱是用户的登录标识，唯一索引同时承担业务唯一约束（防止重复注册）与登录查询路径。模型 tag `uniqueIndex` 默认生成同名索引 `idx_user_credentials_email`，与 SQL 一致。

---

### posts（1 个索引，`004_create_post_tables.sql`）

#### 10. `idx_posts_author_created` — 作者帖子游标分页

```sql
ON posts (author_id, created_at DESC)
```

**对应代码**：`repo.GetPostsByAuthor()` / `repo.GetPublishedPostsByAuthor()`

```go
WHERE author_id = ? AND created_at < ? [AND status = ?] ORDER BY created_at DESC LIMIT ?
```

**依据**：作者维度游标分页是帖子流的核心查询。`author_id` 等值过滤占最左前缀；`created_at DESC` 同时承担范围过滤（`< ?` 游标）与排序，B-tree 一索引两用，避免全表扫描和 filesort。`GetPublishedPostsByAuthor` 额外带 `status` 等值过滤时仍走同一索引（扫描时行过滤）；若该查询成为热点，可升级为 `(author_id, status, created_at DESC)` 复合索引，当前遵循"不冗余"原则不预建。

---

### post_medias（1 个索引，`004_create_post_tables.sql`）

#### 11. `idx_post_medias_post_id` — 帖子媒体关联查询 / 删除

```sql
ON post_medias (post_id)
```

**对应代码**：`repo.GetPostMedias()` / `repo.DeletePostMedias()` / `repo.GetPostCover()`

```sql
WHERE post_id = ?   -- 或 WHERE post_id IN (...)
```

**依据**：帖子↔媒体关联的唯一查询入口是 `post_id`（取列表、删关联、取封面均按它过滤），单列索引精确覆盖。单帖媒体数量很小，`sort_order` 排序在结果集内完成，无需复合索引。

---

### user_follows（2 个索引，`005_create_user_tables.sql`）

表上约束：

- **CHECK** `chk_user_follows_no_self`（`fan_id <> following_id`）：防止自关注
- **外键** `fk_user_follows_following`（`following_id` → `user_profiles.id`）、`fk_user_follows_fan`（`fan_id` → `user_profiles.id`），均 `ON UPDATE CASCADE ON DELETE CASCADE`：用户注销/删除时由 DB 级联清理关注关系，模型侧对应 `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE"` tag
- **已有库兜底**：`CREATE TABLE IF NOT EXISTS` 不会为已存在的表追加约束，`005` 尾部以幂等 `ALTER TABLE ... DROP CONSTRAINT IF EXISTS / ADD CONSTRAINT` 重建同名外键；要求表内无孤儿数据（`fan_id`/`following_id` 均存在于 `user_profiles.id`），否则 `ADD CONSTRAINT` 失败
- **计数维护**：级联删除只清理关系行，不回写 `user_profiles.followers/followings` 计数，计数由应用层维护

> `user_follows` 是全库唯一建 DB 级外键的表（其余表沿用应用层一致性约定）——关注关系对用户行强归属且注销时必须级联清理，交给 DB 保证。

#### 12. `idx_user_follows_fan_following` — 防重复关注 + 关注列表

```sql
ON user_follows (fan_id, following_id)  -- UNIQUE
```

**对应查询**：

```sql
WHERE fan_id = ? AND following_id = ?   -- 判重 / 取关
WHERE fan_id = ?                        -- "我关注的人"列表（最左前缀覆盖）
```

**依据**：复合唯一索引同时承担唯一约束，从 DB 层杜绝重复关注，无需额外建唯一约束。`fan_id` 在前（等值过滤，最左前缀覆盖"我关注的人"查询），`following_id` 在后（等值过滤，判重/取关精确匹配）。

---

#### 13. `idx_user_follows_following_id` — 粉丝列表

```sql
ON user_follows (following_id)
```

**对应查询**：

```sql
WHERE following_id = ?   -- "我的粉丝"列表
```

**依据**：复合唯一索引最左前缀为 `fan_id`，无法覆盖 `following_id` 的反向查询，需单列索引。与索引 12 不冗余：两个方向的等值查询各占一个最左前缀。模型中通过 `gorm:"index:idx_user_follows_following_id"` tag 声明，与 SQL 同名。

> **预留**：若后续 repo 落地游标分页（`WHERE fan_id = ? AND created_at < ? ORDER BY created_at DESC`），需补充 `(fan_id, created_at DESC)` / `(following_id, created_at DESC)` 复合索引。当前无对应查询代码，遵循"不做猜测性索引"原则暂不建。

---

### fanout_timelines（2 个索引，`009_create_fanout_tables.sql`）

#### 14. `idx_fanout_timelines_uid_cursor` — 收件箱 keyset 分页

```sql
ON fanout_timelines (uid, post_created_at DESC, post_id DESC)
```

**对应代码**：`internal/discover/v1/repo.go` `FanoutRepo.ReadInbox`（GetFollowingFeed push 路径，P1 接入）

```sql
WHERE uid = ? AND (post_created_at < ? OR (post_created_at = ? AND post_id < ?))
ORDER BY post_created_at DESC, post_id DESC LIMIT ?
```

**依据**：关注流核心读路径。uid 等值列在最左，后接游标元组 (post_created_at, post_id)（即帖子的
(created_at, id)，与 pull 路径共享游标空间），索引序与排序序一致，keyset 翻页免 filesort。

#### 15. `idx_fanout_timelines_post_created_at` — 保留期清理

```sql
ON fanout_timelines (post_created_at)
```

**对应代码**：`internal/poller/fanout_cleaner.go` `FanoutCleaner`（定时清理，跑在 cmd/poller）→
`internal/discover/v1/repo.go` `FanoutRepo.CleanupExpired`（ctid 批删，批间独立提交）

```sql
DELETE FROM fanout_timelines
WHERE ctid IN (
  SELECT ctid FROM fanout_timelines WHERE post_created_at < ? LIMIT ?
)
```

**依据**：收件箱行数 = 粉丝数 x 发帖数，无限增长，需按帖子时间窗定期清理；
按 post_created_at（帖子时间）而非行创建时间，口径与游标一致（帖子过保留期即从收件箱视图消失）。
不做删帖级联删除（已删帖行由下游 BFF 回查组装与前端过滤），无按 post_id 删除路径故不建索引。

---

## 索引总结表

| # | 索引名 | 表 | 列 | 类型 | 核心查询 |
| --- | -------- | ---- | ---- | ------ | ---------- |
| 1 | `idx_media_assets_created_at` | media_assets | created_at DESC | B-tree | 游标分页 |
| 2 | `idx_media_assets_status_refcount_created` | media_assets | status, ref_count, created_at | B-tree | 未使用媒体清理 |
| 3 | `idx_media_assets_uploader_id` | media_assets | uploader_id | B-tree | 按上传者查询 |
| 4 | `idx_media_files_asset_type` | media_files | asset_id, type | B-tree | 按资产+类型查询 |
| 5 | `idx_outbox_pending_poll` | out_box_entries | status, retry_count, send_at | **部分索引** | Poller 轮询 ⭐ |
| 6 | `idx_outbox_send_at` | out_box_entries | send_at | B-tree | 时间范围查询 |
| 7 | `idx_outbox_trace_id` | out_box_entries | trace_id | B-tree | 链路追踪 |
| 8 | `idx_outbox_subject` | out_box_entries | subject | B-tree | 按主题查询 |
| 9 | `idx_user_credentials_email` | user_credentials | email | **UNIQUE** | 邮箱唯一约束 / 登录 |
| 10 | `idx_posts_author_created` | posts | author_id, created_at DESC | B-tree | 作者帖子游标分页 |
| 11 | `idx_post_medias_post_id` | post_medias | post_id | B-tree | 帖子媒体关联查询/删除 |
| 12 | `idx_user_follows_fan_following` | user_follows | fan_id, following_id | **UNIQUE** | 防重复关注 + 关注列表 |
| 13 | `idx_user_follows_following_id` | user_follows | following_id | B-tree | 粉丝列表 |
| 14 | `idx_fanout_timelines_uid_cursor` | fanout_timelines | uid, post_created_at DESC, post_id DESC | B-tree | 收件箱 keyset 分页 |
| 15 | `idx_fanout_timelines_post_created_at` | fanout_timelines | post_created_at | B-tree | 保留期清理 |

## 设计原则

1. **查询驱动**：每个索引都对应代码中的实际查询路径，不做猜测性索引
2. **部分索引优先**：`outbox_pending_poll` 使用 `WHERE status = 0` 部分索引，仅索引活跃行，显著减小索引体积和维护成本
3. **复合索引列顺序**：等值过滤列 → 范围过滤列 → 排序列，确保 B-tree 最左前缀匹配
4. **不冗余**：主键查询（如 `WHERE id = ?`）依赖聚簇索引，不额外建索引
