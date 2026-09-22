-- ============================================================
-- rednote 索引 SQL
-- 基于代码中的查询模式分析建立
-- 数据库: PostgreSQL
-- ============================================================

-- ============================================================
-- media_assets 表索引
-- ============================================================

-- 索引 1: 游标分页查询
-- 对应 repo.ListMediaAssetsBefore()
--   WHERE created_at < ? ORDER BY created_at DESC LIMIT ?
-- 时间范围过滤 + 降序排序，需要 B-tree 索引
CREATE INDEX IF NOT EXISTS idx_media_assets_created_at
    ON media_assets (created_at DESC);

-- 索引 2: 未使用媒体查询（清理任务）
-- 对应 repo.GetUnusedMedia()
--   WHERE (ref_count <= 0 AND status = 4 AND created_at < ?)
--      OR (ref_count <= 0 AND status = 3 AND created_at < ?)
--   LIMIT ?
-- 需要覆盖 ref_count、status、created_at 三列以支持 OR 条件扫描
CREATE INDEX IF NOT EXISTS idx_media_assets_status_refcount_created
    ON media_assets (status, ref_count, created_at);

-- 索引 3: 按上传者查询（未来扩展：用户查看自己上传的媒体）
-- 当前代码中 UploaderID 已有 gorm:"index" tag
-- GORM AutoMigrate 会创建单列索引 idx_media_assets_uploader_id
-- 此处显式创建以确保一致性
CREATE INDEX IF NOT EXISTS idx_media_assets_uploader_id
    ON media_assets (uploader_id);

-- ============================================================
-- media_files 表索引
-- ============================================================

-- 索引 4: 按资产ID+文件类型联合查询
-- 对应 repo.GetMediaFiles()
--   WHERE (id = ? AND type = ?) OR (id = ? AND type = ?) ...
-- 模型中已有复合索引 idx_media_files_asset_type (asset_id, type)
-- 此处显式创建以确保一致
CREATE INDEX IF NOT EXISTS idx_media_files_asset_type
    ON media_files (asset_id, type);

-- ============================================================
-- out_box_entries 表索引
-- ============================================================

-- 索引 5: Poller 轮询热路径（最关键的查询）
-- 对应 poller.Work()
--   WHERE send_at < now()
--     AND status = 0          -- STATUS_PENDING
--     AND retry_count < maxRetry
--   FOR UPDATE SKIP LOCKED
--   LIMIT batchSize
-- Poller 是高频定时任务，此索引直接决定消息投递延迟
-- status 放首位（等值过滤，基数低），retry_count 次之（范围），send_at 最后（范围+隐式排序）
CREATE INDEX IF NOT EXISTS idx_outbox_pending_poll
    ON out_box_entries (status, retry_count, send_at)
    WHERE status = 0;  -- 部分索引：仅索引待处理行，大幅减小索引体积

-- 索引 6: 按发送时间查询（通用查询）
-- send_at 用于延迟消息调度，单独查询或与其他条件组合
CREATE INDEX IF NOT EXISTS idx_outbox_send_at
    ON out_box_entries (send_at);

-- 索引 7: trace_id 已有 gorm:"index" tag
-- GORM AutoMigrate 会创建 idx_out_box_entries_trace_id
-- 此处显式创建以确保链路追踪查询可用
CREATE INDEX IF NOT EXISTS idx_outbox_trace_id
    ON out_box_entries (trace_id);

-- 索引 8: subject 已有 gorm:"index" tag
-- GORM AutoMigrate 会创建 idx_out_box_entries_subject
-- 此处显式创建以确保按主题查询可用
CREATE INDEX IF NOT EXISTS idx_outbox_subject
    ON out_box_entries (subject);
