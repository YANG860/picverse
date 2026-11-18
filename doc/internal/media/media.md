# media — 媒体服务

代码：`internal/media/`（`models/`、`v1/{biz,repo,service}.go`），proto：`proto/media/v1/`，进程：`cmd/media`（端口 `:8080`）。

管理图片的上传、异步处理（生成多规格变体）、引用计数与回收。核心抽象：

- **MediaAsset（媒体资产）**：来自同一张原图的一组变体的抽象。
- **MediaFile（媒体文件）**：一个具体变体（原图 / 216px / 540px / 1080px webp），**不可变**，创建后只可删除。

## 数据模型

### MediaAsset（表 `media_assets`）
| 字段 | 说明 |
| --- | --- |
| `BaseModel` | 雪花 ID、created_at、updated_at |
| `Status` | 状态（见下） |
| `RefCount` | 引用计数 |
| `UploaderID` | 上传者 ID |

### MediaFile（表 `media_files`）
| 字段 | 说明 |
| --- | --- |
| `BaseModel` | 雪花 ID 等 |
| `AssetID` / `Type` | 所属资产 + 文件类型，复合索引 `(asset_id, type)` |
| `ObjKey` / `Bucket` | S3 对象键 / 存储桶 |
| `Sha256Sum` / `SizeBytes` | 校验信息 |
| `Format` / `Height` / `Width` | 格式 / 高 / 宽 |
| `MetaData` | 扩展元数据（jsonb） |

## 状态机（`MediaAssetStatus`）

proto 枚举取值（注意取值非连续）：

| 状态 | 值 | 含义 |
| --- | --- | --- |
| `UNSPECIFIED` | 0 | — |
| `UPLOADING` | 1 | 上传中 |
| `AUDITING` | 2 | 审核中 |
| `ACTIVE` | 3 | 已完成（可用） |
| `FAILED` | 4 | 上传/校验失败 |
| `PROCESSING` | 5 | 处理中 |
| `ERROR` | 6 | 处理出错 |
| `BANNED` | 7 | 已封禁 |
| `DELETED` | 8 | 已删除（待回收） |

流转：`UPLOADING → AUDITING → PROCESSING → ACTIVE`；校验不通过 `→ FAILED`；封禁 `→ BANNED`；回收 `→ DELETED`。状态更新为**条件更新**（`SetMediaAssetStatus(id, from, to)`），保证并发下按预期流转。

## 接口（MediaService）

| RPC | 说明 |
| --- | --- |
| `UploadMedia` | 创建资产 + 返回预签名直传 URL + 写延时上传检测消息 |
| `GetMediaAssets` | 按 ID 批量获取资产（逐项 `MediaAssetResult`） |
| `GetMediaAsset` | 按 ID 获取单个资产（单值结果，非法 ID → `InvalidID`，未命中 → `NotFound`） |
| `GetMediaAssetFiles` | 按 `(id, type)` 批量获取文件（逐项 `MediaAssetFileResult`） |
| `GetMediaAssetFile` | 按 `(id, type)` 获取单个文件（单值结果，非法 ID → `InvalidID`，未命中 → `NotFound`） |
| `SignMediaFileURLs` | 批量签发展示 GET URL（逐项独立签发、非原子——部分失败为正常语义，逐项 `fail`，TTL 分级：默认 24h / 显式至 7d 上限，超限 `InvalidArgument`；纯读不改引用计数，BFF 聚合后内嵌 MediaView） |
| `ListMediaAssetsBefore` | 按时间游标分页列资产 |
| `BanMedia` | 封禁（置 `BANNED` + 发 MQ 消息删对象） |
| `ModifyMediaAssetRefCount` | 原子修改引用计数（全成功或全失败） |
| `DeleteUnusedMedia` | 标记未使用资产为 `DELETED` 并发删除消息 |

### UploadMedia 流程
1. 格式过滤：原图只接受 **PNG / JPG**。
2. 事务内：创建 `MediaAsset(UPLOADING)` → 生成预签名 PUT URL → **立即**写 `upload_media` 消息。
3. 客户端用预签名 URL 直传 MinIO（不经服务端）。

**上传确认（重试语义）**：消息在事务内立即投递，consumer 先在 handler 内短轮询等对象落地
（`defaults.UploadObjectWait` = 2s，100ms 一查）——通常几百毫秒内就接住客户端刚 PUT 的对象，
省掉一次 Nak 退避重投；仍未到则按 `NakWithDelay` 逐条退避（1s/2s/3s/5s）重投，
窗口耗尽（`max_retry` 次投递）仍未到 → 判上传失败（`UPLOADING → FAILED`）。仅 `UPLOADING` 可推进（守卫防重复处理）。

变体处理阶段（`PROCESSING`）失败同样有耗尽兜底：投递次数用尽后置 `FAILED` 并 ACK，
而不是让消息被 JetStream 静默丢弃（那会让资产永久停在 `PROCESSING`）。
前端提示“上传完成”：直传完成后轮询 `GetMediaAsset`（间隔 1~2s），状态离开 `UPLOADING`（进 `AUDITING`）即已确认。

### 引用计数
`ModifyMediaAssetRefCount` 接收 `{id, delta}` 列表，在单事务内逐项 `ref_count += delta`，整体原子。帖子引用/释放媒体时调用。

### DeleteUnusedMedia
选取 `ref_count <= 0` 且状态为 `ACTIVE` 或 `FAILED` 且创建超过 15 分钟的资产，标记为 `DELETED`，并尽力而为地发 `delete_media` 消息（事务外，失败不影响结果）。

## 上传/处理流水线（异步）

上传后的图片由 [consumer](../consumer/consumer.md) 异步处理，状态推进在**同一次消息投递内联完成**：

```
UploadMedia ──▶ upload_media ──▶ 校验原图 + 落库
                                    │
                                    ▼
         UPLOADING ─▶ AUDITING（真实审核接入点，当前占位）─▶ PROCESSING ─▶ 三档变体 ─▶ ACTIVE
```

> 历史实现把链路拆成 `upload → audit → process → publish` 四次消息接力，每跳都要等事务 outbox 的
> poller tick（秒级 × 3 跳），实测「PUT 完成 → ACTIVE」被拖到 11s+，而审核与发布两步都只有占位逻辑。
> 现在只有 `upload_media` 一条消息，状态推进走直接的状态守卫转移（`ProcessAsset`）；
> 流中残留的 `audit_media` / `process_media` / `publish_media` 消息由兼容入口幂等消费（不会重复转换）。

另有旁路消息 `deref_media`（引用计数 -1）：BFF 在挂载点替换（换头像/换草稿媒体）时发出，
由 consumer 直接递减计数（不涉及状态机），卸载路径异步化、最终一致。

### 图片预处理
- 校验合法性（大小、SHA256、实际格式、尺寸，见 consumer 的 `ValidateImage`）。
- 保存原图。
- 生成定宽压缩变体：宽度 **216px / 540px / 1080px**（原图只解码一次，三档复用同一份解码结果）。
  - 仅当原图宽度大于目标宽度时缩放（Lanczos），小图不放大。
  - 处理后统一编码为 **webp**（质量 85）。
- 原图只接受 png / jpg，处理后变体统一 webp。

> 备注：早期设计中提到"压缩裁剪、合法长宽比 1:4 至 4:1"等规则，当前实现仅做**定宽缩放**，未做裁剪与长宽比限制，属规划项。

## 对象键约定

`utils.BuildKey(id, type, format)` 生成 S3 key：`media/<id>/<type>.<ext>`，如 `media/123/origin.png`、`media/123/540.webp`。详见 [packages/utils/utils.md](../../packages/utils/utils.md)。

## 依赖

- `MediaRepository` — 数据访问，支持 `WithTX`
- `mq.JetStreamClient` — 发送事务性消息（`SendTX`）/ 尽力而为消息（`Send`）
- `oss.S3Repo` — 预签名 URL、下载、上传、删除对象
- 相关消息处理见 [consumer](../consumer/consumer.md)、发件箱见 [common](../common/common.md)
