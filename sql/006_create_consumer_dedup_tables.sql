-- ============================================================
-- 消费端消息幂等去重表（共用机制，所有 AtLeastOnce 消费点复用）
-- 生成自 GORM 模型定义（internal/common/models/dedup.go）
-- 数据库: PostgreSQL
-- ============================================================

-- 收编早期命名（consumer_processed_messages 已废弃）
DROP TABLE IF EXISTS consumer_processed_messages;

-- -----------------------------------------------------------
-- 1. processed_messages — 消费端幂等去重记录
--    与业务副作用同事务登记，保证 AtLeastOnce 投递下的恰好一次效果
--    保留 7 天（远超 ack_wait × max_deliver 重投窗口），待巡检任务建立后清理
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS processed_messages (
    message_id   BIGINT      NOT NULL,                               -- MQMessage.ID（雪花ID）
    handler      VARCHAR(64) NOT NULL,                               -- 处理器标识（如 deref_media）
    processed_at BIGINT      NOT NULL DEFAULT 0,                     -- 处理时间(unix毫秒)
    -- 幂等粒度 = (message_id, handler) 复合：同一消息可被多个消费点（fanout/索引等）
    -- 各自独立幂等，互不误杀
    PRIMARY KEY (message_id, handler)
);

CREATE INDEX IF NOT EXISTS idx_processed_messages_processed_at
    ON processed_messages (processed_at);
