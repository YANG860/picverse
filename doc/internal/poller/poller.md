# poller — 后台轮询作业

代码：`internal/poller/`，进程：`cmd/poller`。常驻轮询类后台作业的统一归属：

- `OutBoxPoller`（poller.go）：outbox → NATS JetStream；
- `RedisPoller`（redis_poller.go）：浏览计数刷回（Redis → posts.view_count）。

## OutBoxPoller

## 结构

`OutBoxPoller` 持有 `*gorm.DB`、`*mq.JetStreamClient`、`OutBoxRepository`，以及可配置的 `workers` / `interval` / `batchSize` / `publishTimeout`。

生命周期：
- `Start()` — 启动 `workers` 个 worker goroutine。
- `Close()` — 取消 context → 停 ticker → 等待 worker 收尾。

## 工作循环（Work）

每个 worker 按 `ticker` 周期执行一次：

1. 在**单事务**内 `Poll` 拉取一批到期待发消息（`send_at < now`、`status = PENDING`、`retry_count < MaxRetry`，行锁 `FOR UPDATE SKIP LOCKED`）。
2. `dispatch` 并发发送这批消息。
3. 对失败项 `AddEntryRetryCount`（累加重试），对成功项 `SetEntryStatusDone`（置 `DONE`）。
4. 事务提交，保证状态更新与发送结果一致。

## 发送（sendMsg / dispatch）

- `sendMsg` 把 outbox 条目还原为 NATS 消息：消息头带 `trace_id` / `send_at` / `message_id`，并合并自定义 `Header`。
- 使用 `PublishMsgAsync` 异步发布，**必须等待 ack future 的真实结果**，不因 ctx 超时提前返回（避免误判发送失败导致重复投递）。
- `dispatch` 用 goroutine 并发发送整批，收集成功/失败 ID；`ctx` 设置 `publishTimeout` 超时。
- 已取消的 context（关闭/超时）不再发出新消息，避免关闭期间产生 in-flight 消息。

## 可靠性要点

| 机制 | 作用 |
| --- | --- |
| Outbox 与业务同事务 | 本地事务成功 ⇔ 消息必定入发件箱 |
| `FOR UPDATE SKIP LOCKED` | 多 worker/多实例并发拉取不冲突 |
| ack future 阻塞等待 | 不重复投递 |
| `retry_count` + 部分索引 | 失败可重试，热路径高效 |

消息体为 protobuf 序列化的 `MediaMessage` 等，投递到 `media` 流后由 [consumer](../consumer/consumer.md) 消费。

## RedisPoller（浏览计数刷回：Redis → posts.view_count）

`RedisPoller` 定时把 Redis 累积的浏览增量原子取走后批量写回 `posts.view_count`（PV 近似，口径见 [packages/cache/cache.md](../../packages/cache/cache.md)）：

- **生命周期**：`Run(ctx)` 阻塞 ticker 循环；ctx 取消即尽力冲刷残留后退出。刷回统一用脱离循环取消的短超时上下文（10s）——关停瞬间在途批次照常完成，不因取消而丢弃。
- **单轮刷回（flushOnce）**：`Cache.TakePostViewDeltas` 取走增量（GETDEL，取走在前）→ 逐帖 `Repo.IncPostViewCount` 写库。
- **只丢不重**：取走失败增量留在 Redis 下轮再试（不丢）；写库失败丢弃不回填（回填有双计风险，宁丢不重）；帖子不存在（RowsAffected=0）丢弃。
- **配置**：`config.post.view_flush_interval`（默认 30s）。
