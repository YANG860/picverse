# consumer — 消息消费者

代码：`internal/consumer/`（`consumer.go`、`media_consumer.go`、`post_consumer.go`、`utils.go`），进程：`cmd/consumer`（同进程拉起两个消费者）。

消费 JetStream 中的两类消息：`media` 流的 `MediaMessage`（驱动图片处理状态机）与 `post` 流的 `PostMessage`（驱动帖子状态机）。采用 **ACK/NAK** 确认机制：处理成功 `Ack`，可重试错误 `Nak`（交回队列重投），不可恢复错误 `Term`。

## MediaConsumer

由 `NewMediaConsumer` 创建，通过 `CreateOrUpdateConsumer` 建立 durable consumer：

| 配置 | 说明 |
| --- | --- |
| `Name` / `Durable` | consumer 名（`config.Consumer.Media.Name`） |
| `DeliverPolicy` | `DeliverAll` |
| `AckPolicy` | `AckExplicit` |
| `MaxDeliver` | 最大投递次数（`max_retry`） |

生命周期：`Start()` 创建消费循环（并确保工作目录存在），`Close()` 取消 context 并停止消费。

## 消息分发（HandleMessage）

1. 从消息头取 `trace_id` / `send_at` / `message_id`，注入 context。
2. `proto.Unmarshal` 出 `MediaMessage`，失败则 `Term`（不可恢复）。
3. 按 `oneof payload` 分发到对应 handler。
4. 依据返回 `(ack, err)` 决定 `Ack` / `Nak`，并记录结构化日志。
5. 全程 `recover` panic，防止单条消息搞挂 worker。

## 消息处理器

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `upload_media` | `HandleUploadMedia` | 校验原图、落 `MediaFile(ORIGIN)`、推进 `UPLOADING→AUDITING` |
| `audit_media` | `HandleAuditMedia` | 审核（当前直通），推进 `AUDITING→PROCESSING` |
| `process_media` | `HandleProcessMedia` | 生成 3 个 webp 变体、落库、推进 `PROCESSING→ACTIVE` |
| `publish_media` | `HandlePublishMedia` | 发布（当前仅记录日志） |
| `delete_media` | `HandleDeleteMedia` | 删除 S3 对象 + 删除 `media_files` / `media_assets` 记录 |
| `ban_media` | `HandleBanMedia` | 封禁后删除各变体的 S3 对象 |

### HandleUploadMedia
1. 检查 S3 原图是否存在，不存在则标记 `FAILED`。
2. 下载原图，`ValidateImage` 校验：大小、SHA256、实际格式、宽高（含 `MaxImageWidth/Height` 上限 8192）、元数据一致性、格式受支持。
3. 校验失败标记 `FAILED`；通过则落 `MediaFile(ORIGIN)`。
4. 发 `audit_media` 并推进状态到 `AUDITING`（状态更新 + 消息写入同事务）。

### HandleProcessMedia
对 216 / 540 / 1080 三种类型：
- `ConvertImage` 解码 → 定宽缩放（Lanczos，仅缩小不放大）→ webp 编码（质量 85）。
- 上传 webp 到 S3，计算 SHA256，解码取尺寸，落 `MediaFile`。
- 全部完成后发 `publish_media`，推进状态到 `ACTIVE`。

### HandleDeleteMedia
按四种类型查出所有 `MediaFile`，逐个删除 S3 对象，再在事务内删除 `media_files` 与 `media_assets` 记录。

## PostConsumer

由 `NewPostConsumer` 创建（依赖 media 服务客户端与 `PostRepository`），消费 `post` 流中的 `PostMessage`，
durable 名取 `config.Consumer.Post.Name`，ACK/NAK 策略与 MediaConsumer 一致。`cmd/consumer` 同进程拉起两个消费者。

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `audit_post` | `handleAuditPost` | 审核（当前占位直通），同事务 `AUDITING→PUBLISHED` + 发 `publish_post` |
| `publish_post` | `handlePublishPost` | 发布后下游处理（占位：仅记录日志，不阻塞 ACK） |
| `delete_post` | `handleDeletePost` | 同事务物理删除软删帖子（仅 `DELETED` 行）及其媒体关联，重复投递幂等 |
| `ban_post` | `handleBanPost` | 封禁后的下游处理 |

## ValidateImage

校验原图，返回错误即视为校验失败（业务失败，标 `FAILED`，不重试）：
- `SizeBytes` 与实际长度一致；
- `Sha256Sum` 与实际一致；
- `image.DecodeConfig` 探测的实际格式与声明 `Format` 一致；
- 宽高为正且 ≤ 8192，且与元数据一致；
- 格式受支持（PNG/JPG/WEBP）。

## ACK 约定

- `ACK = true`：处理完成（含业务失败已妥善落状态），从队列移除。
- `RETRY = false`：可重试错误（如 S3/DB 抖动），`Nak` 后重投。
- 解析失败等不可恢复错误用 `Term`。
