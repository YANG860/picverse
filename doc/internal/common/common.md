# common — 公共模型与 Outbox

代码：`internal/common/`（`models/`、`v1/repo.go`）。

## BaseModel

`models.BaseModel` 是所有 GORM 模型的公共基类：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `ID` | int64 | 雪花 ID 主键（`autoIncrement:false`） |
| `CreatedAt` / `UpdatedAt` | int64 | unix 毫秒时间戳 |

GORM 钩子：
- `BeforeCreate` — 自动填充雪花 ID 与创建时间（为空时）。
- `BeforeUpdate` — 自动更新 `UpdatedAt`。

`ToProto` / `BaseModelFromProto` 与 `common.v1.BaseModel`（string ID）互转。

## Outbox（事务性发件箱）

`models.OutBoxEntry`（表 `out_box_entries`）承载"待发送到 JetStream 的消息"，与业务变更写入**同一事务**，由 [poller](../poller/poller.md) 异步投递，保证本地事务成功 ⇔ 消息最终发出。

| 字段 | 说明 |
| --- | --- |
| `BaseModel` | 雪花 ID 即消息 ID |
| `TraceID` | 链路追踪 ID |
| `Subject` | NATS 主题 |
| `SendAt` | 计划发送时间（unix 毫秒），延迟消息靠它控制 |
| `Header` / `Payload` | NATS 消息头 / protobuf 序列化的消息体 |
| `Status` | 状态（见下） |
| `RetryCount` | 发布重试次数 |

### 状态枚举（`models.Status`）
`PENDING(0)` → `PROCESSEING(1)` → `DONE(2)`，另有 `CANCELED(3)` / `FAILED(4)`。

## OutBoxRepository

`v1.OutBoxRepository`（实现 `OutBoxRepoImpl`）：

| 方法 | 说明 |
| --- | --- |
| `WithTX(tx)` | 绑定事务 |
| `Poll(ctx, batchSize)` | 拉取一批待发消息：`SendAt < now` 且 `Status = PENDING` 且 `RetryCount < MaxRetry`，使用 `FOR UPDATE SKIP LOCKED` 行锁 |
| `SetEntryStatusDone(ids)` | 批量置为 `DONE` |
| `AddEntryRetryCount(ids)` | 批量累加重试次数 |

`Poll` 用 `clause.Locking{Strength: Update, Options: SkipLocked}`，多 worker/多实例并发拉取时不阻塞、不重复竞争同一批行。索引设计见 [sql/README.md](../../../sql/README.md)（`idx_outbox_pending_poll` 部分索引为热路径）。
