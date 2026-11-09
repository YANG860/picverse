# discover 服务（搜推）设计

> 模块：`internal/discover/v1`（RPC / biz）+ `internal/consumer/discover_consumer.go`（索引同步/关注流扇出）。
> 入口 `cmd/discover`（`:8085`）：RPC handler + DiscoverConsumer 同进程装配。

## 职责切分

- discover 只输出 `post_ids` / 排序信息（`proto/discover/v1/requests.proto`，内部接口，BFF 调用）；
  可见性过滤与 `PostCard` 组装收敛在 BFF（复用 SearchPosts 的回查组装路径）。
- `SearchPosts` 已实现（Meilisearch 双排序，offset 游标）；`GetRecommendFeed` 为 P2 占位。

## GetFollowingFeed（关注流，P1 设计）

写扩散/推拉结合：push（`fanout_timelines`，普通作者发帖扇出粉丝收件箱）+ pull（大V发件箱缓存现拉）。
组装时按 **缓存存在性** 分流（见下），按 `(created_at, id)` 复合游标 2 路归并，
`post_id` 去重（宁重不漏），返回排序后的 `post_ids`。

- **面客契约**：`bff.v1.GetFollowingFeedRequest{cursor}` → BFF 拉全量关注列表（封顶）→ 调 discover。
- **待落地**：归并实现 + 复合游标编解码 + 空页语义口径（见 bff.md 待定项）；收件箱/发件箱缓存与发布分流已落地（见下）。
- 注意：`doc/internal/bff/bff.md` 信息流一节的"逐作者 `ViewPostByAuthor` 归并"为**旧方案**，已废弃；
  以本节推拉归并方案与 proto 注释为准。

## 推拉分流（已定稿：缓存存在性 = pull 判据）

**无大V状态表、无宽限期、无滞回**——判据只有两个，均无状态：

| 环节 | 判据 | 动作 |
|---|---|---|
| 写侧（发帖） | `followers >= bigv.promote_followers`（5000） | 大V → 写发件箱缓存；普通 → 扇出收件箱 |
| 读侧（归并） | **发件箱缓存 key 存在**（`Window` miss 分流） | 有缓存（现任大V + 刚降级未过期）→ pull；无缓存 → 收件箱 |
| 降级（跌破线） | 无动作 | 缓存随 TTL（30d）自然消亡，自动回归纯收件箱 |

### 零漏论证（缓存 key 存活 ≥ `fanout.retention` 30d 时）

| 状态 | 帖子去向 | 供给线 | 漏吗 |
|---|---|---|---|
| 普通作者 / 晋升前 | fanout | 收件箱（30d） | 不漏（寿命语义一致） |
| 大V期 | 只写缓存 | key 在 → pull（见底回源 DB） | 不漏 |
| 刚降级（key 未清） | 新帖 fanout、旧帖在缓存 | 双路（pull 见底回源拉到新帖，归并去重兜重） | 不漏 |
| 降级 key 过期后 | 新帖 fanout | 收件箱 | 不漏（旧帖已过寿命） |
| 粉丝数抖动（单阈值无滞回） | 随机走一路 | 各有一条供给线 | 不漏（仅双写重，去重兜底） |

- **零漏前提**：`cache.timeline_ttl`（30d）≥ `fanout.retention`（30d）——两者必须对齐，改一个须对齐另一个。
- 唯一残余风险 = **Redis 数据丢失**（故障/淘汰）导致 key 提前消亡、大V期帖子断供——基础设施风险，已接受；
  缓解手段为 Redis 持久化/主从，不在应用层兜底。
- 晋升首帖 race（key 建立前粉丝读到 → 误分流收件箱）：**接受**，稍等刷新即可（已拍板，不做防护）。

### 发帖侧链路（已落地，`DiscoverConsumer.handlePublishFanout`）

```
PublishPost 消息 → 取帖（PUBLISHED 校验）→ GetUserProfile 取粉丝数
  → followers ≥ 晋升线？ → 是：AuthorTimelineCache.Append
                        → 否：GetFollowers 全量分页 → FanoutRepo.WriteInbox（幂等 ON CONFLICT）
```

- **Append 失败 fallback 扇出**（宁重不漏）：新口径下读侧 miss=收件箱路，帖子必须有收件箱供给线，
  缓存故障时降级全量扇出（写放大仅故障模式），降级自身失败则 RETRY 重投。
- 扇出安全阀 = 晋升线：拉粉丝途中触达晋升线（判定后粉丝激增 race）→ 转写缓存走 pull（漏扇出由 pull 兜底）。
- 幂等：`Dedup.ExecuteIdempotent(msgID, "fanout_publish")` + 各写入天然幂等，重投安全；
  与索引同步（P3）双效应共存，任一侧 RETRY 整体重投、另一侧去重跳过。
- 抖动/首帖/错扇的双写由归并 `post_id` 去重兜底（宁重不漏方向正确）。

### 读侧分流契约（`AuthorTimelineCache.Window`，GetFollowingFeed 接线时使用）

- **miss = true**（key 不存在）→ 非 pull 对象，分流到收件箱路（**不回源**，普通作者零 DB 开销）；
- **miss = false 且短取**（len < limit）→ pull 对象但缓存不能完整作答（见底/同刻游标过滤）→
  回源 DB 补足并 `Rebuild`（pull 对象是大V，回源量小）；短取不等于到底；
- 命中 → 直接用。N 个 following 作者批量 pipeline，一次 RTT。

### 发件箱缓存（`packages/cache` `AuthorTimelineCache`，已落地）

- per 作者 ZSET 窗口：member = `post_id`、score = `created_at`，恰为游标二元组 `(created_at, id)`；
  只保近期 `cache.timeline_capacity` 条（最新连续后缀，默认 200）、TTL `cache.timeline_ttl`（默认 30d，±10% 抖动）。
- **语义 = 加速层**：DB 仍是事实源；key 存在性兼作 pull 分流判据（见上）。
- `Rebuild` 不变式：只能以作者“最新连续后缀”（DB 倒序 LIMIT 回源行）回填（增量 ZADD 不 DEL，
  与并发 Append 合并）；严禁深翻页窗口行回填（制造空洞会把短取误判为完整而漏帖）。
- 不做删帖失效（与收件箱同口径，下游过滤兜底）。
- 不做 Redis Set 名单/逐 EXISTS 判据：`Window` 一次调用同时完成判据与取数（miss 分流），单一事实源无影子失配。

## 装配与配置

- `cmd/gen` `Discover` 装配集注入 `NewFanoutRepo`；DiscoverConsumer 持 `FanoutWriter`/`AuthorTimelineCache`
  做发布分流（`cmd/discover` 同进程，索引同步 + 发布分流双效应）。
- 配置仅一项：`config.yaml` `bigv.promote_followers`（0 回退 `defaults.BigVPromoteFollowers` 5000）。
- 历史注：`discover_bigv_authors` 大V快照表及其镜像仓库（宽限期/滞回方案）已整体退役并从 sql/008 移除
  （判据收敛为缓存存在性后无消费方）。

## 状态

- 已落地：发帖侧推拉分流（判定/扇出/发件箱缓存写入，含 fallback）、收件箱表 `fanout_timelines`（sql/009，
  复合主键 (uid, post_id) 幂等扇出、游标与 pull 共享 (post_created_at, post_id)）与 `FanoutRepo`
  （WriteInbox / ReadInbox keyset 分页，含集成测试）。**不做删帖级联删除**：已删帖行留在收件箱，
  由下游（BFF 回查组装时过滤）与前端自行过滤，行随保留期清理回收。保留期清理已落地：`FanoutCleaner`
 （internal/poller，跑在 cmd/poller，`fanout.retention` 默认 30 天 / `fanout.cleanup_interval` 默认 1h）
  → `FanoutRepo.CleanupExpired`（ctid 批删防长锁，按帖子时间口径，与游标一致）；发件箱缓存
  `AuthorTimelineCache`（packages/cache，ZSET 窗口 + miss 分流契约，含测试）。
- 待实现（P1 剩余）：GetFollowingFeed 2 路归并 + 复合游标编解码（`FanoutRepo.ReadInbox`
  + `Window` miss 分流现拉 + `AuthorTimelineCache.Window/Rebuild`）。
- 待实现（P2）：GetRecommendFeed（热度快照 + seen 去重 + 打散 + 探索位）。
