# consumer — 消息消费者

代码：`internal/consumer/`（`consumer.go` 基座、`media_consumer.go`、`post_consumer.go`、
`discover_index_consumer.go`、`fanout_consumer.go`、`utils.go`）。
进程装配：`cmd/consumer`（Media + Post）、`cmd/discover`（DiscoverIndex + Fanout）。

消费 JetStream 消息，**ACK/NAK** 确认：处理成功 `Ack`，可重试错误 `Nak`（交回队列重投），
不可恢复错误 `Term`。

## MessageLoop 基座（consumer.go）

泛型模板 `MessageLoop[P proto.Message]` 承载消息循环公共流程（`HandleMessage`）：

1. 从消息头取 `trace_id` / `send_at` / `message_id`，注入 context；
2. `proto.Unmarshal` 出消息（`oneof` 类型名取自 proto 反射，用于日志），失败 `Term`（不可恢复）；
3. 分发到各消费者实现的 `dispatch(ctx, m, messageID)`（oneof type switch，业务本质）；
4. 依据返回 `(ack, err)` 决定 `Ack` / `Nak`，记录结构化日志；
5. 全程 `recover` panic，防止单条消息搞挂 worker。

生命周期：`Start()`（可选工作目录准备）建立 durable consumer 消费循环，`Close()` 取消 context 停止消费。
业务依赖（Repo/S3/Dedup 等）由各消费者自持，基座仅持 MQ、日志与运行参数。

`utils.go` 提供消息 ID 解析（`ParseMessageID` 失败 RETRY / `ParseSnowflakeID` 失败 ACK）；
幂等处理统一为 `Dedup.ExecuteIdempotent`（去重登记与副作用同事务，重复投递跳过）原地展开。

## MediaConsumer

`internal/consumer/media_consumer.go`，`cmd/consumer` 进程。消费 `media` 流 `MediaMessage`
驱动图片处理状态机；durable 名取 `config.Consumer.Media.Name`（`DeliverAll` / `AckExplicit` /
`MaxDeliver` = `max_retry`）。

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `upload_media` | `HandleUploadMedia` | 校验原图、落 `MediaFile(ORIGIN)`、推进 `UPLOADING→AUDITING` |
| `audit_media` | `HandleAuditMedia` | 审核（当前直通），推进 `AUDITING→PROCESSING` |
| `process_media` | `HandleProcessMedia` | 生成 3 个 webp 变体、落库、推进 `PROCESSING→ACTIVE` |
| `publish_media` | `HandlePublishMedia` | 发布（当前仅记录日志） |
| `delete_media` | `HandleDeleteMedia` | 删除 S3 对象 + 删除 `media_files` / `media_assets` 记录 |
| `ban_media` | `HandleBanMedia` | 封禁后删除各变体的 S3 对象 |
| `deref_media` | `HandleDerefMedia` | 异步卸载：引用计数 -1（BFF 挂载点替换 / 删帖回收时发出，见 [bff](../bff/bff.md)）；非法资产 ID ACK 丢弃，其余失败 Nak 重投 |

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

`internal/consumer/post_consumer.go`，`cmd/consumer` 进程。消费 `post` 流 `PostMessage`
驱动帖子状态机；durable 名取 `config.Consumer.Post.Name`。

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `audit_post` | `handleAuditPost` | 审核（当前占位直通），同事务 `AUDITING→PUBLISHED` + 发 `publish_post` |
| `publish_post` | `handlePublishPost` | 发布后下游处理（占位：仅记录日志，不阻塞 ACK） |
| `delete_post` | `handleDeletePost` | 同事务物理删除软删帖子（仅 `DELETED` 行）及其媒体关联，并按被删关联逐槽位写 outbox `deref_media` 卸载消息（资产引用计数回收，删几槽减几槽，重复投递幂等） |
| `ban_post` | `handleBanPost` | 封禁后的下游处理 |

## DiscoverIndexConsumer

`internal/consumer/discover_index_consumer.go`，**跑在 `cmd/discover` 进程内**。消费 `post` 流
`PostMessage` 驱动 Meilisearch 索引同步；durable 名取 `config.Consumer.Discover.Index.Name`。
`Start()` 先 `EnsurePostsIndex` 幂等确保 `posts` 索引存在并应用设置（fail-fast），再建立消费循环。
索引文档组装/设置收敛在 `packages/meili`（`NewPostIndexDoc` / `PostsIndexSettings`），
consumer 侧仅做 proto→`map` 字段提取（`postIndexFields`）薄编排。

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `publish_post` | `handleUpsertPostIndex` | 拉 `post.GetPostsByIDs` → 提取字段建 `PostIndexDoc` → `UpsertPost` 写 `posts` 索引（仅 PUBLISHED；非 PUBLISHED/结果缺失降级跳过并 Warn） |
| `ban_post` / `delete_post` | `handleRemovePostIndex` | `RemovePost` 按主键删除索引文档（删不存在的键为 no-op） |
| `audit_post` | — | 审核中间态不进索引（仅 PUBLISHED 进索引），直接 ACK |

幂等双保险：`Dedup.ExecuteIdempotent`（msgID 去重登记与索引写入同事务，重复投递跳过）+ Meili 按主键
upsert/delete 天然幂等；索引任务阻塞至生效（`WaitForTask`），失败 `Nak` 重投。文档 schema 与索引设置见
[meili](../../packages/meili/meili.md)。

## FanoutConsumer

`internal/consumer/fanout_consumer.go`，**跑在 `cmd/discover` 进程内**。消费 `post` 流 `PostMessage`
做关注流推拉分流（见 [discover](../discover/discover.md)）；durable 名取 `config.Consumer.Discover.Fanout.Name`。
与 DiscoverIndexConsumer 拆为独立 durable（失败隔离：各自重试预算/死信/进度）。

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `publish_post` | `handlePublishFanout` | 取帖（PUBLISHED 校验）→ 查粉丝数 → 防抖动判定分流：大V写发件箱缓存（`AuthorTimelineCache.Append`，失败 fallback 全量扇出）/ 普通作者 `GetFollowers` 全量分页扇出收件箱（`FanoutRepo.WriteInbox`） |
| `audit_post` / `ban_post` / `delete_post` | — | 扇出只关心发布事件（删帖不级联收件箱，由保留期回收），直接 ACK |

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
