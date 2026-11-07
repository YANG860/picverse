# cache — 缓存层（Cache-Aside）与帖子/用户实体缓存

代码：`packages/cache/`。通用缓存原语 + 实体缓存（帖子、用户）：key 构造、JSON 序列化、空哨兵、TTL 抖动。错误一律上抛由调用方降级（fail-open），不在本层吞错。

## Cache 原语接口

| 方法 | 说明 |
| --- | --- |
| `Get` / `MGet` / `Set` / `Del` | KV 存取（MGET 对齐返回；`ttl <= 0` 不过期；Del 幂等） |
| `IncrCounter(key, ttl)` | 计数器 +1 并刷新 TTL（原子；key 不存在从 1 起）——多写者并发安全的增量累加 |
| `TakeCounters(prefix)` | 按前缀原子取走计数器增量（取走即清除，多消费者互斥：取走者独得），返回 `key → 十进制原值` |

实现：`RedisCache`（go-redis 映射，`NewCache(cfg, lf)` 按 `config.cache.enabled` 决定建连或注入 `Noop`）、`Noop`（全空转，服务可无 redis 启动）、测试替身 `test/testredis`（内存实现，支持 TTL 语义与操作计数）。

约定：`KeyPrefix = "rn:v1"`（v1 为序列化 schema 版本，不兼容变更升版本整体切换 key 空间）；`NullSentinel` 空哨兵缓存"查询结果为空"防穿透；`JitterTTL` ±10% 抖动防雪崩。

## PostEntityCache（帖子实体缓存）

主体（非计数列，含状态与媒体关联）与统计列分块存储，key：`rn:v1:post:detail:{id}` / `rn:v1:post:stat:{id}`。

- **读**：`LoadPostDetails` / `LoadPostStats` 批量读（MGET 对齐）——id 不在返回 map 中为 miss（回源后 `Store*` 回填）；`map[id] == nil` 为空哨兵命中（确认不存在，防穿透）。
- **写**：`StorePostDetail` / `StorePostStat`（`detail/stat == nil` 写空哨兵）。
- **失效**：`DelPostDetails` / `DelPostStats`（幂等，写后失效登记用）。

### 浏览计数器（PV 近似，只丢不重）

- `IncrPostViewCount(id)`：+1 到独立计数器 key `rn:v1:post:view:{id}`（不与缓存实体混写，避免缓存失效误伤计数）；TTL 24h 安全网（防刷回长期停摆堆积），每次 +1 刷新。
- `TakePostViewDeltas()`：按前缀原子取走全部待刷回增量（`id → delta`）；非法 key/值（结构性垃圾）取走即弃并留 Warn。
- 口径：只丢不重——取走在前写库在后（消费者见 [internal/poller/poller.md](../../internal/poller/poller.md) 的 RedisPoller）。

## UserEntityCache（用户实体缓存）

与 PostEntityCache 同一定式：`rn:v1:user:profile:{id}` / `rn:v1:user:stat:{id}`，API 对称（Load/Store/Del × profile/stat）。
