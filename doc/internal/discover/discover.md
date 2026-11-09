# discover 服务（搜推）设计

> 模块：`internal/discover/v1`（RPC / biz / BigVRepo）+ `internal/consumer/discover_consumer.go`（索引同步/关注流扇出）。
> 入口 `cmd/discover`（`:8085`）：RPC handler + DiscoverConsumer 同进程装配。

## 职责切分

- discover 只输出 `post_ids` / 排序信息（`proto/discover/v1/requests.proto`，内部接口，BFF 调用）；
  可见性过滤与 `PostCard` 组装收敛在 BFF（复用 SearchPosts 的回查组装路径）。
- `SearchPosts` 已实现（Meilisearch 双排序，offset 游标）；`GetRecommendFeed` 为 P2 占位。

## GetFollowingFeed（关注流，P1 设计）

写扩散/推拉结合：push（`fanout_timelines`，名单外作者发帖扇出粉丝收件箱）+ pull（名单内现拉）。
组装时以 `BigVRepo.JoinPullSet(followings)` 分流，按 `(created_at, id)` 复合游标 2 路归并，
`post_id` 去重（宁重不漏），返回排序后的 `post_ids`。

- **面客契约**：`bff.v1.GetFollowingFeedRequest{cursor}` → BFF 拉全量关注列表（封顶）→ 调 discover。
- **待落地**：归并实现 + 复合游标编解码 + 空页语义口径（见 bff.md 待定项）；收件箱表与仓库已落地（见下）。
- 注意：`doc/internal/bff/bff.md` 信息流一节的"逐作者 `ViewPostByAuthor` 归并"为**旧方案**，已废弃；
  以本节推拉归并方案与 proto 注释为准。

## 大V判定（读侧已落地，判定写侧随发布链路落地）

推拉边界靠 `discover_bigv_authors` 快照表（**判定口径单一事实源**）：名单内作者（现任大V + 宽限期降级者）
发帖不走 fanout 扩散、由 pull 现拉；名单外写扩散。

### 判定时机：发布帖子时判定（P1）

- **不扫 user 表、无后台扫描任务**：作者是不是大V，在其**发帖时**判定并维护快照表
  （晋升/降级/宽限清理随 fanout 发布链路落地，本期不实现）。
- 表语义（模型 `internal/discover/models/models.go`）：**行存在 = 在 pull 现拉名单**（现任 + 宽限期降级者）；
  降级写 `demoted_at`（降级时刻/宽限清理锚点）并保留宽限期供 pull 兜底
  （晋升期帖子未扩散，立即删行会被 pull 漏掉），宽限过期物理删；宽限行复用晋升时重置 `demoted_at = NULL`。
- pull 现拉名单**不按晋升时间裁剪**（宁重不漏）：大V/宽限期作者在游标窗内的帖子全拉，
  重复帖由归并 `post_id` 去重；`became_bigv_at` 仅留审计与未来裁剪优化。

### 读侧：BigVRepo 惰性镜像（`internal/discover/v1/repo.go`）

- **惰性更新**：距上次回源超过 `sync_interval` 的首次读触发一次全量回源，否则走内存快照——
  外部视角 ≈ **稍微滞后（<= sync_interval）的无感直连 DB 查询**。
- 失败语义与直连一致：回源失败错误上抛且不推进同步点（下次读重试），无后台 goroutine/ticker。
- 并发语义：原子不可变快照（`atomic.Pointer` 整体替换），读路径无锁；**首次回源完成前读阻塞等待**
  （空快照不是滞后数据，当作"无大V"会错判分流），之后回源期间读旧快照不阻塞，
  过期读用 TryLock 抢回源权（并发过期读只放行一个回源，零排队）。
- **名单口径统一**（现任 + 宽限期降级者合一集合）：在名单内 → 发帖不扩散、pull 现拉；
  不在 → 写扩散/走收件箱。宽限期降级者同样跳过扩散由 pull 兜底，无重复无遗漏。
  API 为集合运算：`IsInPullSet(ctx, uid)` 单点判定（发布时 fanout 跳过判定）；
  `JoinPullSet(ctx, uids)` 交集分流（保持输入顺序，命中走 pull、未命中走收件箱）——
  调用方永远拿自己的关注列表来问，无需全量名单，故无全量输出接口。
- 表小（大V占比，千~万级）：回源 = 一次 `Pluck("uid")` 构建成员集合，读时零排序零阻塞。

### 发件箱缓存（pull 现拉加速层，`packages/cache` `AuthorTimelineCache`，已落地）

- per 作者 ZSET 窗口：member = `post_id`、score = `created_at`，恰为游标二元组 `(created_at, id)`；
  只保近期 `cache.timeline_capacity` 条（最新连续后缀，默认 200）、TTL `cache.timeline_ttl`（默认 7d，±10% 抖动）。
- **语义 = 加速层（方案 A：名单统一缓存，暂不分级）**：DB 仍是事实源，外部语义依然是无感直连 DB。
  `Window` miss（未缓存）或短取（返回 < limit，触及缓存低边界）→ 调用方回源 DB 作答；
  宁重不漏：短取不等于到底，缓存不完整作答时必须 DB 补足。
- `Rebuild` 不变式：只能以作者“最新连续后缀”（DB 倒序 LIMIT 回源行）回填（增量 ZADD 不 DEL，
  与并发 Append 合并）；严禁深翻页窗口行回填（制造空洞会把短取误判为完整而漏帖）。
- 不做删帖失效（与收件箱同口径，下游过滤兜底）；写侧接线在 P1 发布链路（`Append`），
  读侧接线在 GetFollowingFeed 归并（`Window`/`Rebuild`）。大V进一步分级（热大V缓存、
  小大V直连）留作量级演进，判据 = followers（快照列已有）。

### 装配与配置

- `cmd/gen` `Discover` 装配集注入 `NewBigVRepo`（`DiscoverUsecaseImpl.BigV` 为 pull 接入点）；
  发布时判定落地后由 DiscoverConsumer 持仓库做 fanout 判定（届时按需共享实例，惰性同步下
  多实例分歧窗口 <= sync_interval）。
- 配置仅一项：`config.yaml` `bigv.sync_interval`（0 回退 `defaults.BigVSyncInterval` 1m）。

## 状态

- 已落地：大V快照表（sql/008）、`BigVRepo`（惰性镜像，含集成测试）、config `bigv` 段、wire 装配、
  `DiscoverUsecaseImpl.BigV` 接入点；收件箱表 `fanout_timelines`（sql/009，复合主键 (uid, post_id) 幂等扇出、
  游标与 pull 共享 (post_created_at, post_id)）与 `FanoutRepo`（WriteInbox 幂等扇出 / ReadInbox keyset 分页，
  含集成测试）。**不做删帖级联删除**：已删帖行留在收件箱，由下游（BFF 回查组装时过滤）与前端自行过滤，
  行随保留期清理回收。保留期清理已落地：`FanoutCleaner`（internal/poller，跑在 cmd/poller，
  `fanout.retention` 默认 30 天 / `fanout.cleanup_interval` 默认 1h）→ `FanoutRepo.CleanupExpired`
  （ctid 批删防长锁，按帖子时间口径，与游标一致）；发件箱缓存 `AuthorTimelineCache`
 （packages/cache，ZSET 窗口 + miss/短取回源契约，含测试）。
- 待实现（P1）：发布帖子时判定（快照表维护：晋升/降级/宽限清理）、扇出消费（DiscoverConsumer 消费发布事件，
  `JoinPullSet` 分流后 `WriteInbox` 写扩散）、GetFollowingFeed 2 路归并 + 复合游标编解码（`FanoutRepo.ReadInbox`
  + `JoinPullSet` 分流现拉）、`IsInPullSet` 在发布时 fanout 跳过判定的实际消费。
- 待实现（P2）：GetRecommendFeed（热度快照 + seen 去重 + 打散 + 探索位）。
