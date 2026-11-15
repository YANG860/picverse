# discover 服务（搜推）

> 模块：`internal/discover/v1`（RPC / biz）+ `internal/consumer`（`DiscoverIndexConsumer` 索引同步 /
> `FanoutConsumer` 关注流扇出，独立 durable）。
> 入口 `cmd/discover`（`:8085`）：RPC handler + 双消费者同进程装配。

## 职责切分

- discover 只输出 `post_ids` / 排序信息（`proto/discover/v1/requests.proto`，内部接口，BFF 调用）；
  可见性过滤与 `PostCard` 组装收敛在 BFF（复用 SearchPosts 的回查组装路径）。
- `SearchPosts`：Meilisearch 双排序（综合/最新），offset 游标。
- `GetHotPosts`：热榜（热池全序翻页）；`GetRecommendFeed`：推荐流（热池候选 + seen 去重 + 打散 + 探索位）。
  热池（`rn:v1:hot:posts`）的入池/维护/热度分见 `plan_recommend_hot.md`（施工计划，落地部分如下）。

## GetFollowingFeed（关注流）

写扩散/推拉结合：push（`fanout_timelines`，普通作者发帖扇出粉丝收件箱）+ pull（大V发件箱缓存现拉），
按 `(created_at, id)` 复合游标 2 路归并，`post_id` 去重，返回排序后的 `post_ids`。

- **面客契约**：`bff.v1.GetFollowingFeedRequest{cursor}` → BFF 拉全量关注列表（封顶）→ 调 discover。

### 推拉分流（判据 = 缓存存在性）

无大V状态表、无宽限期、无滞回——判据只有两个，均无状态：

| 环节 | 判据 | 动作 |
|---|---|---|
| 写侧（发帖） | `followers >= bigv.promote_followers`（5000）或大V标识在（防抖动保持期） | 大V → 写发件箱缓存；普通 → 扇出收件箱 |
| 读侧（归并） | 发件箱缓存 key 存在（`Window` miss 分流） | 有缓存 → pull；无缓存 → 收件箱 |
| 降级（跌破线） | 无动作 | 大V标识（`bigv.mark_ttl`，7d）期内仍写缓存保连续；标识过期后回归纯收件箱，缓存随 TTL（30d）自然消亡 |

### 防抖动（大V标识）

粉丝数在晋升线附近抖动会使供给路径频繁切换（缓存/收件箱交替）导致发帖缓存造洞，以
**大V标识 key**（`rn:v1:author:bigv:{uid}`，纯 TTL 哨兵）稳定路径：

1. 发帖查粉丝数（GetUserProfile）；
2. `followers >= 晋升线`：设标识 + 刷新 TTL（`bigv.mark_ttl`）→ 写发件箱缓存；
3. `followers < 晋升线` 但标识在：仍写发件箱缓存（不刷新 TTL，窗口自然过期）；
4. `followers < 线` 且无标识：扇出收件箱。

成为大V后至少 `mark_ttl` 天内供给路径稳定；跌破线后标识自然过期回归扇出。
标识读写失败 → RETRY 重投（瞬时错误，不静默降级）。

### 供给线覆盖（承诺窗口 30d 内零漏）

承诺窗口 = `fanout.retention`（30d）；窗口外（30d 前）统一消失（收件箱清理 / 缓存截尾 + TTL，两边同语义）。

| 状态 | 帖子去向 | 供给线 |
|---|---|---|
| 普通作者 / 晋升前 | fanout | 收件箱（30d） |
| 大V期 | 只写缓存 | pull（缓存 = 30d 窗口全量，无条数截断） |
| 刚降级（key 未清） | 新帖 fanout、旧帖在缓存 | 双路（归并去重兜重） |
| 降级 key 过期后 | 新帖 fanout | 收件箱 |
| 粉丝数抖动（单阈值无滞回） | 随机走一路 | 各有一条供给线（双写由去重兜底） |

- **对齐约束**：`cache.timeline_ttl`（30d）= `fanout.retention`（30d），改一个须对齐另一个。
  两者同源使"缓存 key 存在 ⇔ 该作者有承诺窗口内的帖子"等价（key 消亡时刻 = 作者最后帖子出窗时刻）。
- 残余风险：Redis 数据丢失（故障/淘汰）导致 key 提前消亡、大V期帖子断供——基础设施风险，
  由 Redis 持久化/主从缓解，不在应用层兜底。
- 晋升首帖 race（key 建立前粉丝读到 → 误分流收件箱）：不做防护，稍等刷新即可。

### 读路径（`GetFollowingFeed`，零回源 2 路归并）

```
GetFollowingFeed(uid, following_ids, cursor)
├─ 路A 收件箱：FanoutRepo.ReadInbox(uid, cursor, limit)        ← 普通作者供给（DB 即源）
└─ 路B pull：foreach author ∈ following_ids:
       AuthorTimelineCache.Window(author, cursor, limit)
         miss=true   → 跳过（普通作者/超窗不活跃，收件箱路供给或不在承诺范围）
         miss=false  → 直接归并；短取 = 承诺窗口内见底，免回源
└─ 归并 top-limit + post_id 去重，短页即到底（游标置空）
```

零回源依据：缓存 = 承诺窗口内全量（按时间截尾），无条数截断 → 短取 ⇔ 承诺内真见底；
条数保险丝（`cache.timeline_capacity`，默认 5000）仅防事故刷帖。

**全局单游标** `(created_at, post_id)`（`utils.ParseMergeCursor`/`NewMergeCursor`，编码 "ts:id"）：
各路同游标严格早于过滤，翻页游标 = 本页末条元组；到底判定 = 产出 < limit 即到底（游标置空，
proto：游标为空即到底（末页可带数据））。删帖行不剔除（`post_ids` 原样返回，BFF 回查过滤兜底）。

容错口径：缓存读失败不当作 miss（会丢大V供给线）→ 整体失败重试。

### 热榜 / 推荐流读路径

- **热榜 `GetHotPosts`**：`HotPool.TopN`（热度降序，offset 游标），短取即到底（池内全序、
  不剔除已删——BFF 过滤兜底）；不做 seen/个性化（榜单 = 可重复看的排行）。
- **推荐流 `GetRecommendFeed`**：候选扫描（`limit × scan_batch_factor` 批次）→ `SeenCache.Filter`
  去重 → 同作者打散（滑窗 `scatter_window` 内同作者 ≤ 1）→ 探索位（`explore_ratio` 比例随机采样，
  过 seen、去本页重复）→ 曝光记账（`SeenCache.Mark`）。
  **短取 ≠ 到底**（seen/打散跳候选）：批内消费位置推进游标，批用尽且批短（扫到池尾）才是到底。
  容错：候选/seen 失败整体重试（seen 失败不当"未曝光"防重复、不丢记账）；探索位采样失败降级跳过。
- 发帖钩子：`FanoutConsumer` 发布分流后 `HotPool.Add`（0 分占位，新帖秒级可被探索位采样；
  失败 Warn 降级不阻塞发布主链路）。

### 发帖侧链路（`FanoutConsumer.handlePublishFanout`）

```
PublishPost 消息 → 取帖（PUBLISHED 校验）→ GetUserProfile 取粉丝数
  → supplyViaCache 判定（防抖动三分支）
      → 是：AuthorTimelineCache.Append
      → 否：GetFollowers 全量分页 → FanoutRepo.WriteInbox（幂等 ON CONFLICT）
```

- **Append 失败 fallback 扇出**：读侧 miss=收件箱路，帖子必须有收件箱供给线，缓存故障时
  降级全量扇出（写放大仅故障模式），降级自身失败则 RETRY 重投。
- **不设扇出安全阀**：判定与拉粉丝是秒级窗口，粉丝差异个位数，以判定为准照常扇出。
- 幂等：各写入天然幂等（upsert/ON CONFLICT/ZADD）+ 去重登记（msgID）；
  抖动/降级边界的双写由归并 `post_id` 去重兜底。

### 读侧分流契约（`AuthorTimelineCache.Window`）

- **miss = true**（key 不存在）→ 非 pull 对象，分流到收件箱路（普通作者/超窗不活跃）；
- **miss = false** → pull 对象，缓存即承诺窗口内全量，直接参与归并；短取（len < limit）= 承诺内见底，免回源；
- 缓存读失败不当作 miss → 整体失败重试。

### 发件箱缓存（`packages/cache` `AuthorTimelineCache`）

- per 作者 ZSET：member = `post_id`、score = `created_at`，恰为游标二元组 `(created_at, id)`；
  按时间截尾（`fanout.retention` 窗口外 member 移除，窗口内全量）+ 条数保险丝 + TTL
  `cache.timeline_ttl`（±10% 抖动）。
- 语义 = 加速层：DB 仍是事实源；key 存在性兼作 pull 分流判据（见上）。
- 不做删帖失效（与收件箱同口径，下游过滤兜底）。

### 收件箱（`fanout_timelines`，sql/009）

复合主键 `(uid, post_id)`（扇出幂等：ON CONFLICT DO NOTHING），游标列 `(post_created_at, post_id)`
与 pull 路共享二元组。**不做删帖级联删除**：已删帖行留在收件箱，下游（BFF 回查组装时过滤）兜底，
行随保留期清理回收。

保留期清理：`FanoutCleaner`（internal/poller，跑在 cmd/poller，`fanout.retention` 30d /
`fanout.cleanup_interval` 1h）→ `FanoutRepo.CleanupExpired`（ctid 批删防长锁，按帖子时间口径，与游标一致）。

## 装配与配置

- **双 durable 消费者拆分**（失败隔离：各自重试预算/死信/进度）：`cmd/discover` 同进程跑
  `DiscoverIndexConsumer`（Meili 索引同步）与 `FanoutConsumer`（推拉分流），各自订阅 post 流、
  独立确认，互不拖累。
- `cmd/gen` `Discover` 装配集注入 `NewFanoutRepo`；`FanoutConsumer` 持 `FanoutRepo`/`AuthorTimelineCache`，
  `DiscoverIndexConsumer` 持 `MeiliRepo`。
- 配置：`config.yaml` `bigv.promote_followers` / `bigv.mark_ttl`；
  `consumer.discover.index/fanout`（各自 durable 名与 `max_retry`）。
