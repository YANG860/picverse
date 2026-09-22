# 数据库建表与索引设计报告

## 文件说明

| 文件 | 用途 |
|------|------|
| `001_create_tables.sql` | 建表 DDL（3 张表，含字段注释） |
| `002_create_indexes.sql` | 索引 DDL（8 个索引，含部分索引） |

---

## 数据模型总览

共 **3 张表**，均使用雪花 ID 作为主键，时间字段为 unix 毫秒：

| 表名 | 用途 | 模型来源 |
| ------ | ------ | ---------- |
| `media_assets` | 媒体资产（一个原图 + 所有变体的抽象） | `internal/media/models/media.go` |
| `media_files` | 媒体文件（原图/216px/540px/1080px 具体变体） | `internal/media/models/media.go` |
| `out_box_entries` | Outbox 消息发件箱（事务性可靠消息投递） | `internal/common/common/outbox.go` |

---

## 索引设计与依据

### media_assets（3 个索引）

#### 1. `idx_media_assets_created_at` — 时间分页查询

```sql
ON media_assets (created_at DESC)
```

**对应代码**：`repo.ListMediaAssetsBefore(ctx, t, limit)`

```go
WHERE created_at < ? ORDER BY created_at DESC LIMIT ?
```

**依据**：游标分页是核心查询模式，需要按 `created_at` 范围过滤 + 降序排序。B-tree 索引同时覆盖过滤和排序，避免全表扫描和 filesort。

---

#### 2. `idx_media_assets_status_refcount_created` — 未使用媒体清理

```sql
ON media_assets (status, ref_count, created_at)
```

**对应代码**：`repo.GetUnusedMedia(ctx, limit)`

```go
WHERE (ref_count <= 0 AND status = 4 AND created_at < ?)   -- FAILED
   OR (ref_count <= 0 AND status = 3 AND created_at < ?)   -- ACTIVE
LIMIT ?
```

**依据**：这是定时清理任务的查询，三列组合可精确覆盖两个 OR 分支。`status` 放首位（等值过滤，基数最低），`ref_count` 次之（范围），`created_at` 最后（时间阈值过滤）。考虑过拆成两个部分索引，但 OR 条件合并为一个复合索引更简洁。

---

#### 3. `idx_media_assets_uploader_id` — 按上传者查询

```sql
ON media_assets (uploader_id)
```

**对应代码**：模型定义 `gorm:"index"` tag

**依据**：当前代码中 `uploader_id` 已声明为索引列。虽然当前无直接查询使用，但作为用户维度的查询入口（如"查看我的上传"），保留此索引以支持未来扩展。

---

### media_files（1 个索引）

#### 4. `idx_media_files_asset_type` — 资产文件联合查询

```sql
ON media_files (asset_id, type)
```

**对应代码**：`repo.GetMediaFiles(ctx, ids, types)`

```go
WHERE (id = ? AND type = ?) OR (id = ? AND type = ?) ...
```

**依据**：每次查询一组 `(asset_id, type)` 对，复合索引可精确匹配。模型中已通过 `index:idx_media_files_asset_type` tag 声明。`AssetID` 在前（高基数、等值匹配），`Type` 在后（低基数、等值匹配）。

---

### out_box_entries（4 个索引）

#### 5. `idx_outbox_pending_poll` — Poller 轮询热路径 ⭐ 最关键

```sql
ON out_box_entries (status, retry_count, send_at)
WHERE status = 0;  -- 部分索引
```

**对应代码**：`poller.Work()`

```go
WHERE send_at < now()
  AND status = STATUS_PENDING    -- 0
  AND retry_count < maxRetry
FOR UPDATE SKIP LOCKED
LIMIT batchSize
```

**依据**：这是整个系统最关键的查询——Poller 每隔 `interval` 执行一次，直接决定消息投递延迟。

- **部分索引** `WHERE status = 0`：已完成/失败的消息占绝大多数，部分索引仅索引待处理行，索引体积从全表缩小到活跃行
- 列顺序 `status → retry_count → send_at`：`status` 已通过部分索引过滤；`retry_count` 为范围过滤（`< maxRetry`）；`send_at` 为范围过滤 + 隐式排序方向

---

#### 6. `idx_outbox_send_at` — 按发送时间查询

```sql
ON out_box_entries (send_at)
```

**依据**：`send_at` 用于延迟消息调度，除 Poller 热路径外，也可能用于运维排查、监控面板等场景。单独索引支持通用的时间范围查询。

---

#### 7. `idx_outbox_trace_id` — 链路追踪查询

```sql
ON out_box_entries (trace_id)
```

**对应代码**：模型定义 `gorm:"index"` tag

**依据**：支持通过 trace_id 追踪一条消息从创建到投递的全链路，是排查问题的核心查询入口。

---

#### 8. `idx_outbox_subject` — 按主题查询

```sql
ON out_box_entries (subject)
```

**对应代码**：模型定义 `gorm:"index"` tag

**依据**：支持按 NATS 主题过滤消息（如查看特定消息类型的积压情况），运维和监控场景需要。

---

## 索引总结表

| # | 索引名 | 表 | 列 | 类型 | 核心查询 |
| --- | -------- | ---- | ---- | ------ | ---------- |
| 1 | `idx_media_assets_created_at` | media_assets | created_at DESC | B-tree | 游标分页 |
| 2 | `idx_media_assets_status_refcount_created` | media_assets | status, ref_count, created_at | B-tree | 未使用媒体清理 |
| 3 | `idx_media_assets_uploader_id` | media_assets | uploader_id | B-tree | 按上传者查询 |
| 4 | `idx_media_files_asset_type` | media_files | asset_id, type | B-tree | 按资产+类型查询 |
| 5 | `idx_outbox_pending_poll` | out_box_entries | status, retry_count, send_at | **部分索引** | Poller 轮询 ⭐ |
| 6 | `idx_outbox_send_at` | out_box_entries | send_at | B-tree | 时间范围查询 |
| 7 | `idx_outbox_trace_id` | out_box_entries | trace_id | B-tree | 链路追踪 |
| 8 | `idx_outbox_subject` | out_box_entries | subject | B-tree | 按主题查询 |

## 设计原则

1. **查询驱动**：每个索引都对应代码中的实际查询路径，不做猜测性索引
2. **部分索引优先**：`outbox_pending_poll` 使用 `WHERE status = 0` 部分索引，仅索引活跃行，显著减小索引体积和维护成本
3. **复合索引列顺序**：等值过滤列 → 范围过滤列 → 排序列，确保 B-tree 最左前缀匹配
4. **不冗余**：主键查询（如 `WHERE id = ?`）依赖聚簇索引，不额外建索引
