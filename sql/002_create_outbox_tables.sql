-- ============================================================
-- outbox 相关表
-- 生成自 GORM 模型定义（internal/common/models/outbox.go）
-- 数据库: PostgreSQL
-- ============================================================

-- -----------------------------------------------------------
-- 1. out_box_entries — Outbox 消息表
--    事务性消息发件箱，由 Poller 轮询投递到 NATS JetStream
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS out_box_entries (
    id          BIGINT          NOT NULL PRIMARY KEY,              -- 雪花ID
    created_at  BIGINT          NOT NULL DEFAULT 0,                -- 创建时间(unix毫秒)
    updated_at  BIGINT          NOT NULL DEFAULT 0,                -- 更新时间(unix毫秒)

    trace_id    VARCHAR(255)    NOT NULL,                          -- 链路追踪ID
    subject     VARCHAR(255)    NOT NULL,                          -- NATS 主题
    send_at     BIGINT          NOT NULL,                          -- 计划发送时间(unix毫秒)

    header      JSON,                                               -- NATS 消息头
    payload     BYTEA           NOT NULL,                          -- NATS 消息体(protobuf序列化)

    status      INTEGER         NOT NULL DEFAULT 0,                -- 状态(0待处理/1处理中/2已完成/3已取消/4失败)
    retry_count INTEGER         NOT NULL DEFAULT 0                 -- 发布重试次数
);

-- ============================================================
-- 索引（查询驱动）
-- ============================================================

-- 索引 1: Poller 轮询热路径（最关键的查询）
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

-- 索引 2: 按发送时间查询（通用查询）
-- send_at 用于延迟消息调度，单独查询或与其他条件组合
CREATE INDEX IF NOT EXISTS idx_outbox_send_at
    ON out_box_entries (send_at);

-- 索引 3: trace_id 已有 gorm:"index" tag
-- GORM AutoMigrate 会创建 idx_out_box_entries_trace_id
-- 此处显式创建以确保链路追踪查询可用
CREATE INDEX IF NOT EXISTS idx_outbox_trace_id
    ON out_box_entries (trace_id);

-- 索引 4: subject 已有 gorm:"index" tag
-- GORM AutoMigrate 会创建 idx_out_box_entries_subject
-- 此处显式创建以确保按主题查询可用
CREATE INDEX IF NOT EXISTS idx_outbox_subject
    ON out_box_entries (subject);
