# comment — 评论服务设计（0→1，设计稿·未实现）

> 状态：**设计定稿，未动工**。本文档为评论服务从 0 到 1 的完整设计；实现落地后迁移至 `doc/internal/comment/comment.md` 并补测试/文档索引。
> 设计基调（用户确认）：**两级楼中楼 + 评论点赞 + 删除/封禁回收 + 帖子计数联动，尽可能简化、快速实现**；图片评论、@提及、评论审核等砍到 v2。

---

## 1. 概述与范围

### 1.1 职责

comment 服务负责帖子下的评论域：**根评论 + 楼中楼回复（二级封顶）**、评论点赞、评论软删/封禁与物理回收、以及帖子评论数（`posts.comment_count`）的联动同步。uid 由 auth 服务分配后透传（对齐 post/user 模式），本服务不管理凭证；BFF 是唯一面客入口。

### 1.2 MVP 范围（做）

| 能力 | 说明 |
| --- | --- |
| 根评论 | 发表/删除（软删）/封禁（管理侧），创建即可见 |
| 楼中楼回复 | 回复挂根评论（二级封顶），可引用楼内任意一条（`parent_id` 定位展示"回复 @X"） |
| 折叠展示 | 根评论分页，每根附带前 2 条回复预览 + `reply_count`，展开按根翻页 |
| 评论点赞 | 点赞/取消（幂等）+ `like_count`，明细落表 |
| 删除/封禁回收 | 软删（作者）/封禁（管理）→ outbox 消息 → consumer 物理回收，对齐 post 删帖链路 |
| 帖子计数联动 | 评论可见数变化 → outbox 消息 → consumer 同步 `posts.comment_count`（`post.SyncPostStat`） |

### 1.3 明确砍掉（v2 扩展，不在本期）

图片评论（media 资产引用计数）、@提及/通知、评论编辑、评论审核（AUDITING 状态机，见 §3.2）、楼主删他人评论、我的评论/我的点赞列表、举报、频控防刷、三级以上嵌套。

### 1.4 与现有治理的一致性（复用不新造）

| 治理项 | 本服务的对齐方式 |
| --- | --- |
| ID / 时间 / 分页 | 雪花 ID（proto string / DB int64）、unix 毫秒、`common.v1.Cursor` 游标分页（limit ≤ 1000） |
| 错误契约 | `fail` + `common.v1.Error`（可枚举码），不可枚举走连接层 gRPC error |
| 事务性消息 | 业务变更与 outbox 同事务（`MQ.SendTX`），poller → JetStream `comment` 流 |
| 消费幂等 | `processed_messages`（`Dedup.ExecuteIdempotent`）+ 条件更新守卫，AtLeastOnce 下恰好一次效果 |
| 分层 | `service.go`（薄 handler）→ `biz.go`（事务边界/状态流转/错误映射）→ `repo.go`（gorm gen + `WithTX`/`Transaction`）→ `models/`（`ToProto`/`FromProto`） |
| 跨服务校验归属 | 帖子可见性/存在性由 **BFF** 调 `post.ViewPost` 裁决（同媒体归属校验归 BFF 的治理）；本服务只校验**自有数据**（根评论状态、归属） |
| 日志 | 按 `doc/log.md` 分级与字段规范（错误必记、降级必记、消息收发审计收敛两条内） |

---

## 2. 领域模型

### 2.1 两级楼中楼

```
根评论 A (root_id=0, parent_id=0)             ── 游标分页单位
  ├─ 回复 A1 (root_id=A, parent_id=A)          ── "回复 @根作者"
  └─ 回复 A2 (root_id=A, parent_id=A1)         ── "回复 @A1作者"（引用楼内一条）
根评论 B                                       ── 下一页
```

- **单表承载**：根评论与回复同表（`comments`），`root_id` 区分：根评论 `root_id = 0`，回复 `root_id = 所属根评论 ID`。
- **二级封顶**：回复的回复不加深楼层——`root_id` 仍指向根，`parent_id` 指向被回复的评论（仅用于展示"回复 @X"与定位）。深度恒 ≤ 2，查询无递归。
- **冗余 `parent_author_id`**：回复行冗余存储被回复者 uid（用户改名不影响），BFF 据此批量取 `UserBrief` 组装"回复 @X"，无需回查父评论本体。
- **折叠预览**：根评论列表每根附带**前 2 条回复**（时间正序）+ `reply_count`；展开走 `ListReplies` 按根翻页。

### 2.2 状态机（简化：无审核态）

```
             ┌── DeleteComment（作者软删，根则级联）
VISIBLE ─────┤
   (1)       └── BanComment（管理封禁，根则级联）
                      │
        DELETED(3) / BANNED(2)  ←── 终态，对外不可见
```

- `CommentStatus`：`UNSPECIFIED(0) / VISIBLE(1) / BANNED(2) / DELETED(3)`。**创建即 VISIBLE**（MVP 无审核；v2 若要审核，对齐 post 加 `AUDITING` + `audit_comment` 消息占位即可，状态机留扩展位）。
- 软删 = `DELETED`（保留行供物理回收链路），封禁 = `BANNED`；两者对所有查询不可见。
- **级联口径**：根评论删除/封禁 → 其下回复同状态级联（帖子楼层整体消失，对齐小红书语义）；回复删除/封禁仅影响自身。

### 2.3 计数体系与口径（三个计数，来源各异）

| 计数 | 位置 | 维护方式 | 一致性 |
| --- | --- | --- | --- |
| `comments.like_count` | 评论行 | 点赞/取消**同事务**条件更新（明细 `comment_likes` 为准） | 强一致 |
| `comments.reply_count` | 根评论行 | 回复增删/级联**同事务**条件更新（仅根评论维护） | 强一致 |
| `posts.comment_count` | post 库 | **跨服务异步**：outbox → `comment` 流 → CommentConsumer → `post.SyncPostStat` | 最终一致 |

口径定义：

- `like_count` = 该评论 VISIBLE 状态下的点赞人数（点赞时校验评论 VISIBLE）。
- `reply_count` = 根评论下 **VISIBLE 回复数**（不含根本身；回复删除/封禁 -1，级联时整组修正）。
- `posts.comment_count` = 帖子下 **全部 VISIBLE 评论数（根 + 回复）**。任何改变可见评论数的动作（创建根/回复、删除、封禁、级联）都触发一条联动消息，见 §5.2。

---

## 3. 数据模型

代码：`internal/comment/models/models.go`；DDL：`sql/007_create_comment_tables.sql`（幂等，风格对齐 `004_create_post_tables.sql`）。

### 3.1 `comments` — 评论表（根 + 回复同表）

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `BaseModel` | — | 雪花 ID、created_at、updated_at（`common.BaseModel`） |
| `post_id` | BIGINT | 所属帖子 ID（雪花） |
| `author_id` | BIGINT | 评论者 ID |
| `root_id` | BIGINT | 根评论 ID；**根评论 = 0**，回复 = 所属根 ID |
| `parent_id` | BIGINT | 直接父（根 = 0）；回复指向被回复的评论（根或楼内回复） |
| `parent_author_id` | BIGINT | 被回复者 uid（冗余；根 = 0） |
| `content` | VARCHAR(1024) | 评论内容（rune 计数 ≤ 500，`utils.ValidateComment` 校验；列宽按 UTF-8 上限留量） |
| `status` | INTEGER | `CommentStatus`（0/1/2/3） |
| `like_count` | BIGINT | 点赞数（明细 `comment_likes` 为准） |
| `reply_count` | BIGINT | 直接回复的 VISIBLE 数（**仅根评论行维护**，回复行恒 0） |

不设 metadata（本期无扩展元数据需求，砍掉；v2 需要再 `ADD COLUMN`）。

### 3.2 `comment_likes` — 点赞明细

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | BIGINT | 雪花 ID |
| `created_at` / `updated_at` | BIGINT | unix 毫秒 |
| `comment_id` | BIGINT | 评论 ID |
| `user_id` | BIGINT | 点赞者 |

**唯一约束 `uk_comment_likes_comment_user (comment_id, user_id)`**：防重复点赞（`OnConflict DoNothing` 天然幂等）。

### 3.3 索引（查询驱动，注释对齐 sql/*.sql 风格）

```sql
-- 索引 1（热路径）: 根评论游标分页
-- 对应 repo.GetCommentRoots()
--   WHERE post_id = ? AND status = 1 AND created_at < ?
--   ORDER BY created_at DESC, id DESC LIMIT ?
CREATE INDEX IF NOT EXISTS idx_comments_post_created
    ON comments (post_id, created_at DESC, id DESC)
    WHERE root_id = 0 AND status = 1;

-- 索引 2: 楼内回复翻页
-- 对应 repo.GetRepliesByRoot()
--   WHERE root_id = ? AND status = 1 AND created_at > ?
--   ORDER BY created_at ASC, id ASC LIMIT ?
CREATE INDEX IF NOT EXISTS idx_comments_root_created
    ON comments (root_id, created_at ASC, id ASC)
    WHERE root_id <> 0 AND status = 1;

-- 索引 3: 回复预览（每根前 2 条）与级联回收
-- 对应 repo.GetReplyPreviews() / DeleteRootCascade()
--   WHERE root_id IN (...) AND status = 1
CREATE INDEX IF NOT EXISTS idx_comments_root_id
    ON comments (root_id);

-- 索引 4: 点赞状态批量查询
-- 对应 repo.GetLikedSet()
--   WHERE user_id = ? AND comment_id IN (...)
-- 复合唯一约束 uk_comment_likes_comment_user (comment_id, user_id) 左前缀含 comment_id，
-- 该索引服务反向（按 user）查询；"我的点赞"列表（v2）也走此索引
CREATE INDEX IF NOT EXISTS idx_comment_likes_user
    ON comment_likes (user_id, created_at DESC);
```

> 部分索引（`WHERE root_id = 0 AND status = 1`）只索引可见根评论，体积小、命中率高；状态变更（软删/封禁）会移出索引，正好贴合"只翻可见楼层"的查询形态。若后续需要翻管理侧历史（含 BANNED/DELETED），再补全量索引。

### 3.4 DDL 草案

```sql
CREATE TABLE IF NOT EXISTS comments (
    id                BIGINT        NOT NULL PRIMARY KEY,
    created_at        BIGINT        NOT NULL DEFAULT 0,
    updated_at        BIGINT        NOT NULL DEFAULT 0,

    post_id           BIGINT        NOT NULL DEFAULT 0,
    author_id         BIGINT        NOT NULL DEFAULT 0,
    root_id           BIGINT        NOT NULL DEFAULT 0,
    parent_id         BIGINT        NOT NULL DEFAULT 0,
    parent_author_id  BIGINT        NOT NULL DEFAULT 0,

    content           VARCHAR(1024) NOT NULL DEFAULT '',
    status            INTEGER       NOT NULL DEFAULT 0,
    like_count        BIGINT        NOT NULL DEFAULT 0,
    reply_count       BIGINT        NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS comment_likes (
    id          BIGINT NOT NULL PRIMARY KEY,
    created_at  BIGINT NOT NULL DEFAULT 0,
    updated_at  BIGINT NOT NULL DEFAULT 0,

    comment_id  BIGINT NOT NULL DEFAULT 0,
    user_id     BIGINT NOT NULL DEFAULT 0
);

ALTER TABLE comment_likes
    ADD CONSTRAINT uk_comment_likes_comment_user UNIQUE (comment_id, user_id);
```

---

## 4. 内部服务契约（CommentService）

proto：`proto/comment/v1/`（`comment.proto` / `comment_service.proto` / `requests.proto` / `messages.proto`），生成于 `gen/go/comment/v1/`。
进程：`cmd/comment`，端口 **`:8085`**（接线见 §7）。内部服务请求体携带 `uid` / `viewer_id`（面客由 BFF 会话注入，对齐 post）。

### 4.1 消息类型（`comment.proto`）

```proto
// Comment 评论（根与回复同构；root_id/parent_id 为空串即根评论）
message Comment {
  string comment_id = 1;
  string post_id = 2;
  string author_id = 3;
  string root_id = 4;             // 根评论为空
  string parent_id = 5;           // 根评论为空
  string parent_author_id = 6;    // 被回复者（冗余，组装"回复 @X"）；根为空
  string content = 7;
  CommentStatus status = 8;
  CommentStat stat = 9;
  string created_at = 10;
  string updated_at = 11;
  bool liked = 12;                // viewer 是否已赞（查询接口按 viewer_id 回填，写接口无意义）
}

message CommentStat {
  int64 like_count = 1;
  int64 reply_count = 2;          // 仅根评论有意义
}

message CommentThread {           // 根评论 + 回复折叠预览
  Comment root = 1;
  repeated Comment replies = 2;   // 时间正序前 N 条（默认 2）
  int64 reply_count = 3;          // 全部可见回复数（展开翻页依据）
}

enum CommentStatus {
  COMMENT_STATUS_UNSPECIFIED = 0;
  COMMENT_STATUS_VISIBLE = 1;
  COMMENT_STATUS_BANNED = 2;
  COMMENT_STATUS_DELETED = 3;
}
```

### 4.2 RPC 清单（9 个）

| RPC | 语义 | 关键规则 |
| --- | --- | --- |
| `CreateComment` | 发表根评论或回复 | `root_id` 为空 = 根评论；非空 = 回复（`parent_id` 缺省 = 根）。内容校验 → 回复目标校验（存在、VISIBLE、属同帖、二级封顶）→ 同事务落行（回复则根 `reply_count` +1，**对根行 FOR UPDATE**）→ outbox 发计数联动消息。返回完整 `Comment` |
| `GetCommentsByPost` | 根评论游标分页 + 回复预览 | 仅 VISIBLE 根评论，`created_at DESC, id DESC`；每根预览前 2 条 VISIBLE 回复 + `reply_count`；按 `viewer_id` 回填 `liked`。空页 = 空列表 + 空游标（不以 NotFound 表达到底） |
| `ListReplies` | 某根下回复**时间正序**翻页 | 仅 VISIBLE；游标为 after 语义（见 §4.4）；根评论被删/封禁后返回空页 + 空游标（前端随根隐藏） |
| `GetCommentsByIDs` | 批量获取（内部） | 对齐 `PostResult`：`CommentResult{fail, err, comment_id, comment}` 逐项 fail；**无可见性过滤**（BFF 聚合用） |
| `DeleteComment` | 作者删自己的评论（软删） | `uid` 必须为评论作者；回复：自身 DELETED + 根 `reply_count` -1；根：级联 DELETED（根 + 全部回复）+ 根 `reply_count` 置 0。**同事务**发 outbox：物理回收消息 + 计数联动消息。重复调用幂等（0 行命中 → 返回成功） |
| `BanComment` | 管理封禁（无 uid） | VISIBLE→BANNED（条件更新）；根则级联封禁回复。同事务发 outbox：`ban_comment` + 计数联动。重复封禁幂等 |
| `LikeComment` | 点赞（幂等） | 仅可赞 VISIBLE 评论；`OnConflict DoNothing`，0 行 = 已赞，不重复 +1；返回最新 `like_count` |
| `UnlikeComment` | 取消点赞（幂等） | 删除 0 行 = 未赞，不重复 -1；返回最新 `like_count` |
| `GetPostCommentCount` | 帖子可见评论数（内部） | `count(*) WHERE post_id=? AND status=VISIBLE`（根+回复）；计数联动 consumer 取权威值用（§5.2） |

### 4.3 错误码（`packages/errmsg`，评论域 `1500–1599`）

| code | 变量 | 含义 |
| --- | --- | --- |
| 1501 | `InvalidComment` | 评论内容非法（空 / >500 rune / 控制字符） |
| 1502 | `InvalidReplyTarget` | 回复目标不可用（根评论不存在/已删/已封禁/不属该帖，或 parent 不在该楼下） |
| 1503 | `NotCommentable` | 目标评论不可交互（已删/已封禁：回复、点赞时使用） |

通用码复用：`InvalidID`（1001）、`NotFound`（1002）、`Forbidden`（1005）、`InvalidArgument`（1004）。
新增 `utils.ValidateComment(content)`（trim 非空、≤ 500 rune、禁控制字符，放行 `\n\r\t`？**评论不放行换行**——`ValidateComment` 拒绝全部控制字符）与常量 `CommentMaxLen = 500`（与列宽对齐，风格同 `NameMaxLen/BioMaxLen`）。

### 4.4 分页与并发

- **游标**：沿用 `common.v1.Cursor` + `utils.ParseTimeCursor`（时间戳游标 + `id` 二级排序保证确定性，与 post 现状一致）。根评论倒序（`created_at < before`）；**回复正序**为 after 语义，`packages/utils` 补 `ParseTimeCursorAfter` / `NewTimeCursorAfter`（`created_at > after`，limit 规则同现有工具，小工具函数）。
- **并发控制**（对齐 post 的 `LockDraftForUpdate` 模式）：
  - `CreateComment`（回复）/ `DeleteComment` / `BanComment` 涉及 `reply_count` 或级联时，先对**根评论行 `SELECT ... FOR UPDATE`**（`LockRootForUpdate`，WHERE `status=VISIBLE`），原子断言根存在且可见，再改计数/级联；
  - 点赞计数用条件 UPDATE（`like_count = like_count + 1 WHERE id=?`）天然原子；
  - 状态流转一律**条件更新**（`WHERE status=VISIBLE`），0 行命中即幂等跳过（PG 计匹配行、值不变也计 1 的坑，守卫列前置，对齐 `SetPostDeleted` 注释经验）。

### 4.5 可见性裁决分工

| 校验 | 归属 | 方式 |
| --- | --- | --- |
| 帖子存在/可见/已发布（可评论） | **BFF** | `post.ViewPost`（可见性裁决）+ `status == PUBLISHED` 检查 |
| 根评论存在且 VISIBLE、属同帖、二级封顶 | **comment 服务** | 自有数据自校验（`InvalidReplyTarget` / `NotCommentable`） |
| 评论作者身份 | comment 服务 | `uid == author_id`（`Forbidden`） |
| 面客鉴权（token → uid） | BFF | JWT 拦截器 + `sessionUID` 注入 |

---

## 5. 消息链路

### 5.1 消息契约（`comment/v1/messages.proto`，投 `comment` 流）

```proto
message CommentMessage {
  oneof payload {
    DeleteComment delete_comment = 1;             // 物理回收触发（软删后）
    BanComment ban_comment = 2;                   // 封禁后下游处置（占位）
    SyncPostCommentCount sync_post_comment_count = 3; // 帖子评论数联动
  }
}

message DeleteComment { string comment_id = 1; string root_id = 2; string post_id = 3; string author_id = 4; }
message BanComment   { string comment_id = 1; string root_id = 2; string post_id = 3; string author_id = 4; }
// 触发器式：只带 post_id，consumer 现场取权威值（§5.2）
message SyncPostCommentCount { string post_id = 1; }
```

流/常量：`packages/defaults/mq.go` 增 `CommentStream = "comment"`（subject 与流名一致，同 `PostMessageStream` 模式）；consumer 配置 `config.Consumer.Comment{Name, MaxRetry, WorkingDir}`。

### 5.2 帖子计数联动：方案对比与定稿

| 方案 | 机制 | 优点 | 缺点 |
| --- | --- | --- | --- |
| **A. 触发器式（定稿）** | 事务内发 `SyncPostCommentCount{post_id}`；consumer 先调 `comment.GetPostCommentCount` 取权威值，再调 `post.SyncPostStat(comment_count=权威值)` | 乱序/重复投递**全收敛**到当前值（幂等天然）；级联/单条/封禁统一一条消息，不用算 delta；与"本地事务成功 ⇔ 消息必达"治理一致 | consumer 多一次内网 RPC（对齐 PostConsumer 依赖 media 客户端先例） |
| B. 绝对值直带 | 消息带 `{post_id, count}`，consumer 直接 `SyncPostStat` | 少一次 RPC、少一个内部接口 | 并发变更下**乱序覆盖**可能留下持久偏差，需版本比较兜底 |
| C. BFF 同步联动 | BFF 在评论写成功后同步调 `SyncPostStat` | 零新消息/零新 consumer | 跨服务两步写无事务，失败即静默漂移，违背现有计数治理（bff.md"不产生静默漂移"原则）；**不采用** |

**定稿 A**。`SyncPostStat` 现有"覆盖式逐字段更新"语义正好承接；消息只做触发器，值现场取，正确性不依赖投递顺序。代价是联动有秒级延迟（outbox poller `interval: 5s`），符合最终一致预期。

### 5.3 outbox 写入点（业务事务内 `MQ.SendTX`，对齐 `PostUsecaseImpl` 写法）

| 动作 | 同事务发出的消息 |
| --- | --- |
| `CreateComment`（根/回复） | `sync_post_comment_count` |
| `DeleteComment`（回复） | `delete_comment` + `sync_post_comment_count` |
| `DeleteComment`（根，级联） | `delete_comment` + `sync_post_comment_count`（一条即可，级联口径由权威值吸收） |
| `BanComment`（根/回复） | `ban_comment` + `sync_post_comment_count` |
| `LikeComment` / `UnlikeComment` | 无（不改可见评论数） |

### 5.4 CommentConsumer（`internal/consumer/comment_consumer.go`，durable `comment_consumer`）

骨架完全对齐 `PostConsumer`（`CreateOrUpdateConsumer` / ACK-NAK-Term / panic recover / 消息审计日志两条内）。`cmd/consumer` 同进程增拉第三个消费者。

| 消息 | 处理器 | 职责 | 幂等 |
| --- | --- | --- | --- |
| `delete_comment` | `handleDeleteComment` | 物理回收：删除软删（DELETED）评论行 + 其 `comment_likes`；根评论则**先查后删**回复行与其点赞（守卫 `status=DELETED`，防误删可见行）。0 行命中 = 重复投递，直接 ACK | `ExecuteIdempotent(msgID, "delete_comment", …)` 同事务登记 |
| `ban_comment` | `handleBanComment` | 占位消费（日志 + ACK），对齐 `handlePublishPost`；下游处置（通知/审计）待需求 | — |
| `sync_post_comment_count` | `handleSyncPostCommentCount` | ① `comment.GetPostCommentCount` 取可见评论数 → ② `post.SyncPostStat(comment_count=)` 覆盖。失败 `Nak` 重投；帖子已物理删（NotFound）→ ACK 丢弃 | 触发器式天然幂等（重复覆盖同值） |

> 全部处理器遵守 ACK 约定：业务失败已妥善落状态 = ACK；可重试（S3/DB/RPC 抖动）= Nak；解析失败 = Term；错误路径必记（含 `id`/`type`/`trace_id`/`post_id`/`comment_id`）。

---

## 6. BFF 面客契约

proto：`proto/bff/v1/comment_requests.proto`（+ `views.proto` 增 `CommentView`）。**请求体零 uid**，身份一律会话注入；连接层错误不外泄（记日志 → `InternalError`）。

### 6.1 面客视图（`views.proto` 增量）

```proto
// CommentView 面客评论视图（comment.v1.Comment 的面客镜像，作者降维为 UserBrief）
message CommentView {
  string comment_id = 1;
  string content = 2;
  UserBrief author = 3;
  // 被回复者（仅楼内回复非空，展示"回复 @X"；取 parent_author_id 的 UserBrief 简版）
  UserBrief reply_target = 4;
  int64 like_count = 5;
  bool liked = 6;
  // unix 毫秒时间戳的字符串形式（与 PostView.created_at 一致）
  string created_at = 7;
}

// CommentThreadView 根评论 + 回复折叠预览
message CommentThreadView {
  CommentView root = 1;
  repeated CommentView replies = 2;  // 前 2 条，时间正序
  int64 reply_count = 3;
}
```

### 6.2 面客 RPC（6 个，`BFFService` 增量）

| RPC | 请求 | 下游调用 | 聚合职责 |
| --- | --- | --- | --- |
| `CreateComment` | `{post_id, content, root_id?, parent_id?}` | `post.ViewPost`（可见 + PUBLISHED 校验）+ `comment.CreateComment`（+ `user.GetUserBrief` 回显作者/回复目标） | 评论者 = 会话用户；回复目标校验由下游裁决（`InvalidReplyTarget` 透传） |
| `GetComments` | `{post_id, cursor}` | `post.ViewPost` + `comment.GetCommentsByPost` + `user.GetUserBrief`（批量作者/回复目标） | 组装 `CommentThreadView[]`；封面级可见性由 ViewPost 裁决（草稿仅作者可见场景下不开放评论） |
| `GetReplies` | `{comment_id, cursor}` | `comment.ListReplies` + `user.GetUserBrief`（批量） | 楼内展开翻页（时间正序） |
| `DeleteComment` | `{comment_id}` | `comment.DeleteComment` | uid 会话注入；软删 + 异步回收/计数联动由下游消息链路闭环 |
| `LikeComment` / `UnlikeComment` | `{comment_id}` | `comment.LikeComment` / `UnlikeComment` | 返回 `{like_count, liked}`（liked 由 BFF 固定回填 true/false），前端免二次查询 |

不面客：`BanComment` / `GetPostCommentCount` / `GetCommentsByIDs`（管理与内部接口，同 `BanPost`/`SyncPostStat` 治理）。

### 6.3 BFF 聚合细节

- **作者视图**：一条评论一个 `UserBrief`；一页根评论（≤ 20）+ 预览回复（≤ 40）收集 uid **去重后一次** `user.GetUserBrief` 批量取（对齐 `GetPost` 并行聚合模式），`reply_target` 同批合并去重。
- **降级**：作者查无（用户注销/异常）→ 该 `UserBrief` 置空 + `Warn` 日志（对齐"列表项缺失跳过/作者置空"降级约定），不打挂列表。
- **失败语义**：签发/聚合任一必选项失败 → 整体 `InternalError`；下游可枚举业务错误原样透传。
- **无媒体**：评论本期纯文本，不涉及 `MediaView`/引用计数（v2 图片评论再复用 `GetAndRefMediaFile`/`DeRefMediaFile`）。

---

## 7. 落地清单（代码接线全景）

按依赖顺序（每步过验证门：`lsp_diagnostics` 无 error → `./build.sh` → 测试）：

| # | 项 | 内容 |
| --- | --- | --- |
| 1 | proto | `proto/comment/v1/{comment,comment_service,requests,messages}.proto`、`proto/bff/v1/comment_requests.proto`、`views.proto` 增 `CommentView/CommentThreadView`；`buf generate` |
| 2 | SQL | `sql/007_create_comment_tables.sql`（幂等 + 索引注释）；`go run ./scripts/dbinit` 重建模板库 |
| 3 | models | `internal/comment/models/models.go`（`Comment`/`CommentLike`，`ToProto`/`FromProto`，状态枚举） |
| 4 | gorm gen | `cmd/gen/gorm/main.go` 挂新模型 → `gen/query` 增 `comments.gen.go`/`comment_likes.gen.go` |
| 5 | errmsg | `packages/errmsg/err.go` 增 1501–1503；`doc/packages/errmsg/err.md` 同步 |
| 6 | utils/defaults | `utils.ValidateComment` + `CommentMaxLen`；`utils.ParseTimeCursorAfter/NewTimeCursorAfter`；`defaults.CommentStream` |
| 7 | repo | `internal/comment/v1/repo.go`：`WithTX/Transaction/DB`、`CreateComment`、`LockRootForUpdate`、`IncReplyCount/DecReplyCount/ZeroReplyCount`、`GetCommentRoots`、`GetReplyPreviews`（窗口函数 `ROW_NUMBER() OVER (PARTITION BY root_id ORDER BY created_at, id) <= 2`，gorm gen 走 `q.Do`/原生扫描；备选：逐根查询 N+1，MVP 可接受）、`GetRepliesByRoot`、`GetComments`、`SetCommentDeleted/Banned`（含级联）、`CreateLike/DeleteLike`、`GetLikedSet`、`CountVisibleByPost` |
| 8 | biz | `internal/comment/v1/biz.go`：9 个 usecase，事务边界 + 状态机 + sentinel 错误映射 + `SendTX` 写法对齐 `PostUsecaseImpl` |
| 9 | service | `internal/comment/v1/service.go`：薄 handler（委托 biz），风格对齐 `post/v1/service.go` |
| 10 | consumer | `internal/consumer/comment_consumer.go`（+ `cmd/consumer` 增拉）；`InternalConsumer` wire 装配 |
| 11 | rpc client | `packages/rpc`：`ServiceComment`、`NewCommentClient`（`provider.go` 静态地址源补 `cfg.Services.Comment`） |
| 12 | config | `ServerConfig.Comment`（`:8085`）、`ServicesConfig.Comment`、`ConsumerConfig.Comment`、`config.yaml` 补条目 |
| 13 | wire | `cmd/gen/wire.go`：`Repos/Usecases/Services` 增 comment 条目、`InitializeCommentServiceHandler`、`InitializeCommentConsumer`；`go generate ./cmd/gen` |
| 14 | cmd | `cmd/comment/main.go`（对齐 `cmd/post/main.go`：trace + validate 拦截器，优雅关闭） |
| 15 | BFF | `internal/bff/v1/biz_comment.go` + `repo.go` DownstreamRepo 增 comment 客户端 + `service.go` 6 个 handler |
| 16 | 测试 | §8 |
| 17 | 文档 | `doc/internal/comment/comment.md`（实现版）+ `doc/doc.md` / `errmsg` / `config` 索引同步 |

---

## 8. 测试计划（对齐 `doc/test/test.md`）

| 层 | 文件 | 依赖 | 覆盖 |
| --- | --- | --- | --- |
| repo 集成 | `internal/comment/v1/repo_test.go` | testdb（模板库克隆） | 根/回复落库、游标分页（倒序/正序、并列时间戳 id 二级序）、回复预览 ≤2、`reply_count`/`like_count` 条件更新、级联软删、条件更新 0 行幂等、部分索引对应查询路径 |
| biz 集成 | `internal/comment/v1/biz_test.go` | testdb + testmq | 9 个 RPC 正常/边界/错误路径：二级封顶（回复的回复挂根）、`InvalidReplyTarget/NotCommentable/Forbidden` 三态、重复删除/封禁/点赞幂等、outbox 消息断言（类型/条数/同事务回滚不留消息）、级联删除计数联动只发一条 |
| consumer | `internal/consumer/comment_consumer_test.go` | testdb + testmq 模拟 + 假 comment/post 客户端 | 物理回收（守卫防误删、重复投递幂等、likes 清理）、计数联动（RPC 失败 Nak、NotFound 丢弃）、`ExecuteIdempotent` 去重 |
| BFF 单测 | `internal/bff/v1/biz_comment_test.go` | 下游 mock（沿用 `biz_post_test.go` 模式） | 会话注入 uid、ViewPost 可见性前置、批量 UserBrief 去重与作者置空降级、错误透传/连接层错误封装 |
| E2E | `test/e2e/comment_e2e_test.go` | 已部署服务（部署流程对齐 `doc/test/test.md`） | 建帖→发评论→回复→折叠/展开→点赞/取消→删除→`posts.comment_count` 最终一致断言→封禁 |

> `sql/*.sql` 变更后必须 `go run ./scripts/dbinit` 重跑模板库。

---

## 9. 里程碑（快速实现排序）

| 里程碑 | 内容 | 出口标准 |
| --- | --- | --- |
| **M1 契约与存储** | proto（§7#1）+ SQL（#2）+ models（#3）+ gen（#4）+ errmsg/utils（#5#6）+ repo（#7） | `buf generate`、`go run ./cmd/gen/gorm`、repo_test 绿 |
| **M2 核心读写** | biz + service + wire + cmd（#8#9#13#14）：CreateComment / GetCommentsByPost / ListReplies / GetCommentsByIDs / Like / Unlike | 单服务起停、biz_test 绿、`./build.sh` 过 |
| **M3 消息与回收** | messages + CommentConsumer + DeleteComment/BanComment/GetPostCommentCount（#10 + 业务侧收尾） | consumer_test 绿，删帖回收链路幂等 |
| **M4 面客与端到端** | BFF 6 RPC（#15）+ e2e（#16）+ 文档（#17） | e2e 绿、计数联动最终一致断言过 |

---

## 10. 风险与开放决策

| # | 项 | 现状决策 | 升级路径 |
| --- | --- | --- | --- |
| 1 | 计数联动延迟 | outbox poller 轮询（`interval: 5s`）→ 秒级最终一致，前端以响应内 `reply_count/like_count` 为准 | 要求更快可缩短 poller 间隔或改 JetStream 直发 + outbox 兜底 |
| 2 | 高热帖计数消息量 | 每次可见数变更一条 `sync_post_comment_count`（trigger 式，含一次 count 聚合） | 同 post 短窗消息合并 / 计数下沉 Redis（`packages/redis` 预留） |
| 3 | 回复预览 N+1 | 定稿窗口函数单查询（`ROW_NUMBER`）；实现困难时退化逐根查询（页 ≤ 20 根） | 合并预览与根分页为一条 LATERAL JOIN |
| 4 | 时间游标并列 | 与 post 现状一致（时间戳游标 + id 二级序，边界并列可能漏项） | 游标升级 `(created_at, id)` 复合编码（`Cursor.str` 扩展格式），全服务统一 |
| 5 | 评论审核 | MVP 无审核态（创建即可见） | 加 `AUDITING` + `audit_comment` 消息（对齐 post 发布链路），状态机已留位 |
| 6 | 防刷/频控 | 无 | v2：同作者同内容短窗唯一约束 / 限频拦截器 |

---

## 附：关键流程时序（创建回复）

```
客户端 ── BFF.CreateComment{post_id, content, root_id, parent_id}
            │ ① post.ViewPost           （可见性裁决：非 PUBLISHED → 对应错误）
            │ ② comment.CreateComment   （uid=会话用户）
            │     biz: 校验内容(1501) → 事务内:
            │        LockRootForUpdate(root)      （根存在且 VISIBLE，否则 1502/1503）
            │        校验 parent 属该楼(1502)、二级封顶(parent 强制归根)
            │        INSERT comment(status=VISIBLE, root_id, parent_id, parent_author_id)
            │        root.reply_count += 1        （条件更新）
            │        outbox: sync_post_comment_count{post_id}   （同事务 SendTX）
            │ ③ user.GetUserBrief（author + parent_author，去重批量）
            └─▶ CommentView
   … poller → JetStream(comment 流) → CommentConsumer
        handleSyncPostCommentCount:
            ① comment.GetPostCommentCount(post_id)   （权威可见数）
            ② post.SyncPostStat(comment_count=权威值) （覆盖式，最终一致收敛）
```
