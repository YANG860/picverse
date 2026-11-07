# redis — Redis 客户端（连接工厂）

代码：`packages/redis/`。go-redis 客户端封装，只负责建连，不做业务语义。

| 类型/方法 | 说明 |
| --- | --- |
| `RedisClient` | go-redis 客户端封装，内嵌 `*goredis.Client`，底层命令可直接调用（如 `r.Get` / `r.SetNX`） |
| `NewClient(cfg, lf)` | 按 `config.redis` 建连，启动 `PING` 校验连通性（失败 fail-fast）；返回 `(*RedisClient, cleanup)` |

业务语义在上层包实现：缓存存取与浏览计数器聚合见 [packages/cache/cache.md](../cache/cache.md)（`Cache` 原语经 `RedisCache` 映射到底层命令）。
