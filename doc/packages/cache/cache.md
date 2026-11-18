# cache — 缓存层（Cache-Aside）与帖子/用户实体缓存

代码：`packages/cache/`。通用缓存原语 + 实体缓存（帖子、用户）：key 构造、JSON 序列化、空哨兵、TTL 抖动。错误一律上抛由调用方降级（fail-open），不在本层吞错。

## Cache 原语接口

| 方法 | 说明 |
| --- | --- |
| `Get` / `MGet` / `Set` / `Del` | KV 存取（MGET 对齐返回；`ttl <= 0` 不过期；Del 幂等） |
| `MSet(pairs, ttl)` | 批量写入并统一 TTL：语义等价逐条 `Set`（不承诺原子，pipeline 单轮 RTT）；空 pairs 零往返 |
| `IncrCounter(key, ttl)` | 计数器 +1 并刷新 TTL（原子；key 不存在从 1 起）——多写者并发安全的增量累加 |
| `TakeCounters(prefix)` | 按前缀原子取走计数器增量（取走即清除，多消费者互斥：取走者独得），返回 `key → 十进制原值` |

实现：`RedisCache`（go-redis 映射，`NewCache(cfg, lf)` 按 `config.cache.enabled` 决定建连或注入 `Noop`）、`Noop`（全空转，服务可无 redis 启动）、测试替身 `test/testredis`（内存实现，支持 TTL 语义与操作计数）。

约定：`KeyPrefix = "rn:v1"`（v1 为序列化 schema 版本，不兼容变更升版本整体切换 key 空间）；`NullSentinel` 空哨兵缓存"查询结果为空"防穿透；`JitterTTL` ±10% 抖动防雪崩。

抖动粒度 = **一次批量写**（一次 `StoreXxxDetails` / `StoreXxxStats` 的 key 集合）：同批 key 共享一个 TTL，"同页 key 一起过期"因此成为「一次批查询 + 一次批量回填」的廉价形状；per-key 错峰反而会把 `IN(20)` 碎成 ~20 次 `IN(1)` + 20 次部分回填。跨批由不同写入时刻 + 各自独立抖动自然分散，全局不会出现同一秒 cliff；冷启动/预热这类"同一秒灌入大量批"的场景应在写入侧限速，而不是把抖动下沉到 per-key。

## 批量 id 工具（batch_ids.go）

缓存装饰器共用的小工具，语义只定义一次，避免两侧口径漂移：

- `DedupeIDs(ids)`：按首次出现顺序去重（MGET 前用：同 id 只取一次 key，返回集每 id 至多一条，且调用方能按原序组装）。
- `SplitHits[V](ids, cached)`：把一次批量读的结果拆成「命中实体 / 命中 id（保持输入序，供短 TTL 统计列取样）/ 未命中 id（→ 回源）」三类；`cached[id] == nil` 为空哨兵（确认不存在：不进任何集合、不回源、不回填）。缓存读失败时传 `nil` 即降级为「全 miss」，仍返回可写 map（调用方会 `maps.Copy` 进去）。

## BatchFlight（批量合并回源，防击穿）

代码：`batch_flight.go`。cache-aside 回源机器的合并原语（已集成：`CachedPostRepo`/`CachedUserRepo` 的详情回源经它合并，替换原集合 key 的 singleflight；`CachedPostRepo` 另建一条统计列 flight）：**per-key 合并 + 在途批合并，零等待**——合并点是 key（并发调用重叠的 id 只回源一次），取数是批（常驻 worker 池把待取 key 打成并集一次 fetch）。不做时间窗口合并（不为凑包加延迟）。

- `NewBatchFlight(maxWorkers, maxBatchSize, fetchTimeout, fetch)`：fetch **构造时绑定**（实例内单源）；参数来源 config（`cache.flight_workers/flight_batch_size/flight_timeout`）> defaults（`defaults.CacheFlight*`）分层，<= 0 回退默认。
- **K 调参得失**：K=1 退回串行波（攒批最优但有队头阻塞）；调大 K 消除队头阻塞、吞吐天花板 ×K，代价是凑批变小（合并率下降）、DB 并发放大。`SetConcurrency(n)` 运行时可调（1 即退回串行波），worker 池只增不减。
- `Do(ctx, keys)`：返回 fetch 确认存在的 key（值共享只读，调用方拷贝组装）；缺 key = 确认不存在（非错误，按空哨兵口径处理）；批失败整批共享同一 error 上抛。
- 发起者取消/Close 不掀翻在途批（fetch 以 `WithoutCancel` 派生 + 自带超时）；fetch 超时/panic/worker 意外 panic 均保证 call 按期 resolve、名额归还、worker 存活（摘表为所有权凭证，无二次 close）。`Close()` 幂等：待取 call 以 `ErrClosed` resolve（不悬挂），在途批照常完成。
- 不缓存结果：只合并并发未命中，回填缓存由调用方负责。`Stats()` 暴露 `Batches/Fetched/Joined/Pending`（`Joined` 即被合并掉的回源数；`Pending` 队列深度持续增长 = 回源供不上，考虑调大 K）。

## PostEntityCache（帖子实体缓存）

主体（非计数列，含状态与媒体关联）与统计列分块存储，key：`rn:v1:post:detail:{id}` / `rn:v1:post:stat:{id}`。

- **读**：`LoadPostDetails` / `LoadPostStats` 批量读（MGET 对齐）——id 不在返回 map 中为 miss（回源后 `Store*` 回填）；`map[id] == nil` 为空哨兵命中（确认不存在，防穿透）。
- **写**：`StorePostDetail` / `StorePostStat`（`detail/stat == nil` 写空哨兵）。批量回填：实体 `StorePostDetails(hits, missing)`（实体/空哨兵 TTL 不同 → 两批 `MSet`）、统计列 `StorePostStats(stats)`（单批 `MSet`）——整批回源从 N 次 SET 降为 ≤ 3 轮 RTT；抖动以批为单位（口径见上文抖动粒度）。
- **失效**：`DelPostDetails` / `DelPostStats`（幂等，写后失效登记用）。
- **统计列（已启用短 TTL 读缓存，B 方案）**：`rn:v1:post:stat:{id}` 独立 TTL（`cache.stat_ttl`，默认 1m ±10% 抖动）；读经 `LoadPostStats` → miss 轻查回填（只写命中值，不写空哨兵：stat 存在 ⟺ 行存在，“不存在”由 detail 层裁决）；**写路径不失效，陈旧 ≤ stat_ttl**；`stat_ttl < 0` 禁用（直通轻查，回退开关）。
- **回源即权威（`CachedPostRepo` 口径）**：`GetPosts` 回源行走全列查询、自带统计列 → 这批 id **不读也不写** stat 缓存（直接采信回源行计数，拿更旧副本覆盖精确值没有意义）；只有**详情缓存命中者**（实体入库时计数已清零）才走 stat 短 TTL 读缓存。因此：整数页回源时统计列零缓存访问、零轻查；同一响应内回源者计数精确、命中者陈旧 ≤ `stat_ttl`。

### 浏览计数器（PV 近似，只丢不重）

- `IncrPostViewCount(id)`：+1 到独立计数器 key `rn:v1:post:view:{id}`（不与缓存实体混写，避免缓存失效误伤计数）；TTL 24h 安全网（防刷回长期停摆堆积），每次 +1 刷新。
- `TakePostViewDeltas()`：按前缀原子取走全部待刷回增量（`id → delta`）；非法 key/值（结构性垃圾）取走即弃并留 Warn。
- 口径：只丢不重——取走在前写库在后（消费者见 [internal/poller/poller.md](../../internal/poller/poller.md) 的 RedisPoller）。

## UserEntityCache（用户实体缓存）

与 PostEntityCache 同一定式：`rn:v1:user:profile:{id}` / `rn:v1:user:stat:{id}`，API 对称（Load/Store/Del × profile/stat，批量回填口 `StoreUserProfiles` / `StoreUserStats`）。统计列同款短 TTL 读缓存（`user:stat:{id}`，语义与 post 一致：写路径（follow/取关）不失效，陈旧 ≤ `stat_ttl`）。

统计列口径与 post 完全一致（回源即权威）：`GetUserProfiles` 回源行走全列查询、自带统计列 → 这批 uid 不读也不写 stat 缓存；只有**资料缓存命中者**（实体入库时计数已清零）才走 stat 短 TTL 读缓存。整页回源时统计列零缓存访问、零轻查；同一响应内回源者计数精确、命中者陈旧 ≤ `stat_ttl`。
