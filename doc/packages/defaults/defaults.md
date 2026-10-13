# defaults — 全局默认常量

代码：`packages/defaults/`。集中存放全局默认值与常量键名，供各包回退使用。

| 文件 | 常量 / 变量 | 值 | 说明 |
| --- | --- | --- | --- |
| `cursor.go` | `CursorLimit` | 20 | 游标分页默认 limit（proto 限制最大 1000） |
| `snowflake.go` | `NodeID` | 0 | 默认雪花节点 ID |
| `jwt.go` | `TokenExpire` | 24h | token 默认有效期 |
| | `JwtAccessToken` / `JwtRefreshToken` | `"access"` / `"refresh"` | token 类型标识 |
| `mq.go` | `MessageIDKey` / `SendAtKey` | `"message_id"` / `"send_at"` | MQ 消息头键 |
| | `MaxRetry` | 5 | Outbox 最大重试次数 |
| `trace.go` | `TraceIDKey` | `"trace_id"` | 链路追踪键（RPC 头 / MQ 头 / 日志字段通用） |
| `media.go` | `MediaMessageStream` / `MediaStream` | `"media"` | JetStream 流/主题名 |
| | `UploadMsgDelay` | 15s | 上传检测延时消息延迟 |
| | `MaxImageWidth` / `MaxImageHeight` | 8192 | 图片尺寸上限 |
| `post.go` | `PostMessageStream` / `PostStream` | `"post"` | JetStream 流/主题名（帖子审核/事件） |
| `s3.go` | `PutUrlExpiry` | 15m | 预签名上传 URL 有效期 |
| `logger.go` | `RotateMaxSize/Backups/Age` | 100 / 7 / 28 | 日志轮转默认参数 |
| | `BufferSize` | 256KB | 日志写缓冲默认大小 |
| `redis.go` | `RedisPoolSize` | 10 | Redis 连接池默认大小 |
| `worker_pool.go` | `WorkerPoolSize` | 10 | 协程池默认容量 |

约定：配置项缺失或非法时，代码回退到此处的默认值。
