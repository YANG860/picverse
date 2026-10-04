-- ============================================================
-- rednote 建表 SQL
-- 生成自 GORM 模型定义（internal/common/common、internal/media/models）
-- 数据库: PostgreSQL
-- ============================================================

-- -----------------------------------------------------------
-- 1. media_assets — 媒体资产表
--    表示来自同一个原图的一组变体图片
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS media_assets (
    id          BIGINT      NOT NULL PRIMARY KEY,                  -- 雪花ID
    created_at  BIGINT      NOT NULL DEFAULT 0,                    -- 创建时间(unix毫秒)
    updated_at  BIGINT      NOT NULL DEFAULT 0,                    -- 更新时间(unix毫秒)

    status      INTEGER     NOT NULL DEFAULT 0,                    -- 状态(MediaAssetStatus枚举)
    ref_count   BIGINT      NOT NULL DEFAULT 0,                    -- 引用计数
    uploader_id BIGINT      NOT NULL                               -- 上传者ID(雪花ID)
);


-- -----------------------------------------------------------
-- 2. media_files — 媒体文件表
--    图片的一个具体变体（原图 / 216px / 540px / 1080px webp）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS media_files (
    id          BIGINT          NOT NULL PRIMARY KEY,              -- 雪花ID
    created_at  BIGINT          NOT NULL DEFAULT 0,                -- 创建时间(unix毫秒)
    updated_at  BIGINT          NOT NULL DEFAULT 0,                -- 更新时间(unix毫秒)

    asset_id    BIGINT          NOT NULL,                          -- 所属媒体资产ID
    type        INTEGER         NOT NULL,                          -- 文件类型(1原图/2 216px/3 540px/4 1080px)

    obj_key     VARCHAR(512)    NOT NULL,                          -- S3对象键
    bucket      VARCHAR(128)    NOT NULL,                          -- S3存储桶

    sha256_sum  VARCHAR(64)     NOT NULL,                          -- SHA256校验和
    size_bytes  BIGINT          NOT NULL,                          -- 文件大小(字节)

    format      INTEGER         NOT NULL,                          -- 媒体格式(1PNG/2JPG/3WEBP)
    height      INTEGER         NOT NULL,                          -- 高度(px)
    width       INTEGER         NOT NULL,                          -- 宽度(px)

    media_meta_data JSONB           NOT NULL DEFAULT '{}'              -- 扩展元数据（与 models.MediaFile.MediaMetaData 对应）
);

-- -----------------------------------------------------------
-- 3. out_box_entries — Outbox 消息表
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
