# consumer — 消息消费者

代码：`internal/consumer/`（`consumer.go` 基座、`media_consumer.go`、`post_consumer.go`、
`discover_index_consumer.go`、`fanout_consumer.go`、`utils.go`）。
进程装配：`cmd/consumer`（Media + Post）、`cmd/discover`（DiscoverIndex + Fanout）。

消费 JetStream 消息，**ACK/NAK** 确认：处理成功 `Ack`，可重试错误 `Nak`（交回队列按全组统一退避节奏重投，
`defaults.ConsumerRetryBackOff`：1s/2s/3s/5s，防瞬时错误重投风暴挤占串行消费循环），
不可恢复错误 `Term`。投递总次数由各消费组 `max_retry`（`MaxDeliver`）控制；
投递序号（`DeliveredFrom(ctx)`，1=首次）供 handler 判“重试耗尽判失败”。

## MessageLoop 基座（consumer.go）

泛型模板 `MessageLoop[P proto.Message]` 承载消息循环公共流程（`HandleMessage`）：

1. 从消息头取 `trace_id` / `send_at` / `message_id`，注入 context；
2. `proto.Unmarshal` 出消息（`oneof` 类型名取自 proto 反射，用于日志），失败 `Term`（不可恢复）；
3. 分发到各消费者实现的 `dispatch(ctx, m, messageID)`（oneof type switch，业务本质）；
   dispatch 前注入**单条消息处理超时** `handleTimeout`（config `consumer.handle_timeout`，默认 30s；
   `<=0` 不限制）：超时 → dispatch 内 `DeadlineExceeded` → `classifyErr` → RETRY 重投（吃 `MaxDeliver` 预算），
   防单条挂死阻塞消息循环；
4. 依据返回 `(ack, err)` 决定 `Ack` / `Nak`，记录结构化日志；
5. 全程 `recover` panic，防止单条消息搞挂 worker。

生命周期：`Start()`（可选工作目录准备）建立 durable consumer 消费循环，`Close()` 取消 context 停止消费。
业务依赖（Repo/S3/Dedup 等）由各消费者自持，基座仅持 MQ、日志与运行参数。

`utils.go` 提供消息 ID 解析（`ParseMessageID` 失败 RETRY / `ParseSnowflakeID` 失败 ACK）
与下游调用错误分类 `classifyErr`（见下文 ACK 约定）；
幂等处理统一为 `Dedup.ExecuteIdempotent`（去重登记与副作用同事务，重复投递跳过）原地展开。

**重试与终态（三路径一序列，见下）**：

| 触发 | 行为 |
| --- | --- |
| handler 返回 `RETRY` | `msg.NakWithDelay(retryDelay(ctx))`：按投递序号取 `[1s,2s,3s,5s]`（超出沿用末值） |
| `handleTimeout`（30s）到期 | ctx deadline → 下游 `DeadlineExceeded` → `classifyErr` 归瞬时 → 同上 |
| handler panic | recover 记 ERROR 后**显式 Nak 同一退避序列**（已 Ack 的不再补 Nak）；不补的话只能靠 `AckWait` 到期重投，节奏不一致 |
| 消息体不可反序列化 | `msg.Term()`：直接终止，不重投 |

总投递次数 = `MaxDeliver = max_retry`；**服务端未配死信，耗尽即静默丢弃**，所以每个消费组必须在耗尽处给业务终态兜底：

| 场景 | 耗尽后的终态 |
| --- | --- |
| `upload_media`：原图始终未落地 | `UPLOADING → FAILED` + ACK（`failAsset`） |
| 媒体处理链失败（S3/DB/解码） | `PROCESSING（或 AUDITING）→ FAILED` + ACK（`processFail` → `failAsset`），错误带原因进 ERROR 日志 |
| `audit_post`：媒体在途 | `AUDITING → DRAFT` + ACK + 一条 Warn（业务裁决，不算失败） |
| `audit_post`：媒体终态不可用 | 立即 `DRAFT` |
| 清除类（`delete_post`/`deref_media`/`delete_media`/`ban_media`）与 panic 场景 | **无兜底**（已知缺口：耗尽后残留 / 需人工介入，见路线图） |

**退避用 `NakWithDelay`，不用 consumer 级 `BackOff`**：NATS 的 `BackOff` 会**覆盖 `AckWait`**，
并把 `ack_wait` 隐式设成 `BackOff[0]`（=1s）——任何耗时超 1s 的 handler 都会在**处理进行中**被判超时重投
（在途重投）；而消费循环是串行的，重投与在途处理交错就造成重复转换/重复扇出（实测同一媒体资产被处理
2~3 次、`fanout_consumer` 累计 263 次重投）。现在：`AckWait = handleTimeout`（单条处理超时 30s，
只兜"进程挂死"，正常 handler 由 ctx 超时自我了断），失败重投的节奏由 `msg.NakWithDelay(retryDelay(ctx))`
逐条下发（按投递序号索引 `defaults.ConsumerRetryBackOff`：1s/2s/3s/5s，超出沿用末值），
总投递次数仍由 `MaxDeliver = max_retry` 控制。

## MediaConsumer

`internal/consumer/media_consumer.go`，`cmd/consumer` 进程。消费 `media` 流 `MediaMessage`
驱动图片处理状态机；durable 名取 `config.Consumer.Media.Name`（`DeliverAll` / `AckExplicit` /
`MaxDeliver` = `max_retry` / `AckWait` = 单条处理超时）。

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `upload_media` | `HandleUploadMedia` | 校验原图、落 `MediaFile(ORIGIN)`，随后**在同一次投递内联**跑 `ProcessAsset`（审核占位 → 变体 → `ACTIVE`） |
| `audit_media` / `process_media` | `HandleAuditMedia` / `HandleProcessMedia` | 兼容入口（旧版四次消息接力的残留消息）：直接委托 `ProcessAsset`，状态守卫保证不重复转换 |
| `publish_media` | `HandlePublishMedia` | 兼容入口：仅记录日志后 `ACK`（已无待办副作用） |
| `delete_media` | `HandleDeleteMedia` | 删除 S3 对象 + 删除 `media_files` / `media_assets` 记录 |
| `ban_media` | `HandleBanMedia` | 封禁后删除各变体的 S3 对象（`assetFiles` 按 `(asset_id, type)` 等长配对取文件，历史实现长度不匹配导致该消息永远失败） |
| `deref_media` | `HandleDerefMedia` | 异步卸载：引用计数 -1（BFF 挂载点替换 / 删帖回收时发出，见 [bff](../bff/bff.md)）；非法资产 ID ACK 丢弃，其余失败 Nak 重投 |

> **为什么状态机不再拆消息**：`upload → audit → process → publish` 接力每跳都要等事务 outbox 的
> poller tick，3 跳实测把「PUT 完成 → ACTIVE」拖到 11s+（其中转换仅 ~0.7s），而审核与发布两步
> 都只有占位逻辑。收敛成一条消息 + 状态守卫直推后，链路时延 = 1 次 poller tick（`poller.interval`）
> + 转换耗时。

### HandleUploadMedia
1. 检查 S3 原图是否存在：消息在 `UploadMedia` 事务里就发出，常比客户端 PUT 早到几百毫秒，
   因此先在 handler 内**短轮询等落地**（`defaults.UploadObjectWait` = 2s，100ms 一查）——就地接住即可
   省掉一次 Nak 退避重投（最小档 1s，是上传链路时延的大头）；仍未到则 `RETRY` 退避重投，
   窗口耗尽（`max_retry` 次投递）仍未到 → 判上传失败（`UPLOADING → FAILED`，守卫转移）。
2. 状态守卫：仅 `UPLOADING` 可推进，重复投递（At-least-once）不重复处理已推进资产。
3. 下载原图，`ValidateImage` 校验：大小、SHA256、实际格式、宽高（含 `MaxImageWidth/Height` 上限 8192）、元数据一致性、格式受支持。
4. 校验失败标记 `FAILED`；通过则落 `MediaFile(ORIGIN)`。
5. 内联调用 `ProcessAsset`（见下），不再发 `audit_media` 消息；已下载并校验过的原图字节直接透传，不重复下载。

### ProcessAsset
一次投递内跑完后续状态机（入口按状态白名单幂等：`ACTIVE`/终态直接 `ACK` 跳过）：
1. `UPLOADING → AUDITING`（真实审核的接入点，当前占位）→ `AUDITING → PROCESSING`。
2. 下载原图并**只解码一次**，对 216 / 540 / 1080 三档：`EncodeThumb` 定宽缩放（Lanczos，仅缩小不放大）
   → webp 编码（质量 85）→ 上传 S3 → 算 SHA256 → 解码取尺寸 → 落 `MediaFile`。
3. `PROCESSING → ACTIVE`（守卫转移，重投时 `RowsAffected=0` 幂等成功）。
4. 任一步失败走 `processFail`：窗口内 `RETRY` 退避；投递次数耗尽则 `在途态 → FAILED` + ACK
   （错误信息带 cause，进 ERROR 日志），避免消息被静默丢弃后资产永久卡在 `PROCESSING`。

### HandleDeleteMedia
按四种类型查出所有 `MediaFile`，逐个删除 S3 对象，再在事务内删除 `media_files` 与 `media_assets` 记录。

## PostConsumer

`internal/consumer/post_consumer.go`，`cmd/consumer` 进程。消费 `post` 流 `PostMessage`
驱动帖子状态机；durable 名取 `config.Consumer.Post.Name`。

| 消息 | 处理器 | 职责 |
| --- | --- | --- |
| `audit_post` | `handleAuditPost` | 发布前置校验帖内媒体全部 `ACTIVE`，判定用**终态白名单**（`FAILED`/`ERROR`/`BANNED`/`DELETED`/`UNSPECIFIED`、资产查无、槽位残缺）→ `ACK` 回退 `DRAFT`；**未列举的状态（含 media 侧未来新增在途态）保守按未就绪 `RETRY`**，避免枚举漂移把在途误判成终态。在途 `RETRY` 重投等就绪，**投递次数达 `max_retry` 仍不就绪则同样回退 `DRAFT`**（JetStream 超 `MaxDeliver` 会静默丢弃消息且无死信，直接 RETRY 会让帖子永久卡 `AUDITING`）。回退属**预期业务裁决**：`ACK` + `nil` 错误 + 一条 Warn 留痕（`post_id`/`rows`/`reason`），不记 Error 以免污染告警。审核仍为占位直通，通过后同事务 `AUDITING→PUBLISHED` + 发 `publish_post` |
| `publish_post` | `handlePublishPost` | 发布后下游处理（占位：仅记录日志，不阻塞 ACK） |
| `delete_post` | `handleDeletePost` | 同事务物理删除软删帖子（仅 `DELETED` 行）及其媒体关联、该帖全部评论行与全部点赞/收藏明细（任意状态一并清，计数随帖子行消失，明细必须清否则污染「我点赞/收藏的帖子」列表），并按被删关联逐槽位写 outbox `deref_media` 卸载消息（资产引用计数回收，删几槽减几槽，重复投递幂等） |
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
| `publish_post` | `handlePublishFanout` | 取帖（PUBLISHED 校验）→ 查粉丝数 → 防抖动判定分流：大V写发件箱缓存（`AuthorTimelineCache.Append`，失败 fallback 全量扇出）/ 普通作者 `GetFollowers` 全量分页扇出收件箱（`FanoutRepo.WriteInbox`）；分流后发帖钩子 `HotPool.Add` 0 分占位入热池（失败降级不阻塞） |
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
- **下游调用错误分类（`utils.go` `classifyErr`）**：
  - 连接层 `Unavailable` / `Unknown` / `DeadlineExceeded` / `ResourceExhausted` 与非 connect 依赖错误（DB/Redis/Meili/S3 抖动）→ `RETRY`（瞬时，重投有意义）；
  - 连接层其余码（`InvalidArgument` / `Unimplemented` / `Internal` 等）→ `ACK`（永久，重投无用，不耗 `MaxDeliver` 预算）；
  - 业务 `fail`（`resp.Msg.GetFail()`）是服务端已裁决的**终态**，不重投（重投只会得到同样裁决）：
    按码处置（如 `NotFound` 降级跳过 + Warn、`InvalidID` / 未知码 `ACK` + Error 人工关注）；
    深层调用经 `errBizFail` 哨兵把终态语义穿透错误链，由上层 `classifyErr` 判 `ACK`。
