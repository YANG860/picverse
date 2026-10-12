# mq — NATS JetStream 客户端

代码：`packages/mq/`（`client.go`、`message.go`）。

封装 NATS JetStream 的消息发送，区分**非事务性**（立即发送）与**事务性**（写 Outbox）两种模式。

## MQMessage

`mq.MQMessage` 是统一的消息载体：

| 字段 | 说明 |
| --- | --- |
| `ID` | 雪花 ID（消息唯一 ID，作为 JetStream 去重 `MsgID`） |
| `Subject` | NATS 主题 |
| `SendAt` | 发送时间（unix 毫秒），延时消息靠它 |
| `Header` | 自定义消息头 |
| `PayLoad` | protobuf 序列化的消息体 |

构造与修饰：
- `NewMQMessage(subject, header, payload)` — 序列化 proto 并生成雪花 ID。
- `WithDelay(t)` — 把 `SendAt` 推后 `t`（延时消息）。
- `WithID(id)` — 覆盖消息 ID。

## MQClient（接口）

`mq.MQClient` 抽象客户端的四个方法 `Send` / `SendTX` / `GetStream` / `Close`，
生产实现为 `*JetStreamClient`（wire 中 `Bind` 到接口）；单元测试注入
`test/testmq` 的内存模拟实现。

## JetStreamClient

`NewJetStreamClient(cfg, lf)` 建立连接（`cfg.JetStream.Url`），返回 `(*JetStreamClient, cleanup)`。

### Send（非事务性）
- **立即**发布到 JetStream，不进 Outbox，不处理延时。
- 消息头写入 `message_id` / `trace_id` / `send_at`，并合并自定义 `Header`。
- 使用 `WithMsgID(雪花ID)` 启用服务端去重。
- 用 `PublishMsg` 同步发布，失败记录错误日志并返回。

### SendTX（事务性）
- 把消息写入 **`out_box_entries` 表**（同一事务 `tx`），由 [poller](../../internal/poller/poller.md) 异步投递。
- 支持延时（`SendAt`），事务提交后才可能被投递。
- `trace_id` 优先取消息头，否则从 ctx 提取。
- 这是"本地事务成功 ⇔ 消息最终发出"的关键。

### 其他
- `GetStream()` — 暴露底层 `jetstream.JetStream`（供 poller 直接 `PublishMsgAsync`）。
- `Close()` — 关闭连接。

异步发布 ack future 设有超时 `asyncPublishAckTimeout = 5s`，避免 ack 丢失时 future 永不 resolve。
