-- ============================================================
-- user 相关表
-- 生成自 GORM 模型定义（internal/user/models/models.go）
-- 数据库: PostgreSQL
-- ============================================================

-- -----------------------------------------------------------
-- 1. user_profiles — 用户资料表
--    uid 由鉴权服务注册时分配，此处建立资料与统计
--    含嵌入的头像媒体文件列（media.MediaFile，前缀 avatar_）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_profiles (
    id          BIGINT       NOT NULL PRIMARY KEY,                   -- 雪花ID（uid）
    created_at  BIGINT       NOT NULL DEFAULT 0,                     -- 创建时间(unix毫秒)
    updated_at  BIGINT       NOT NULL DEFAULT 0,                     -- 更新时间(unix毫秒)

    name        VARCHAR(64)  NOT NULL,                               -- 昵称
    bio         VARCHAR(512) NOT NULL DEFAULT '',                    -- 简介

    followers   BIGINT       NOT NULL DEFAULT 0,                     -- 粉丝数
    followings  BIGINT       NOT NULL DEFAULT 0,                     -- 关注数

    status      INTEGER      NOT NULL DEFAULT 0,                     -- 用户状态(枚举)
    meta_data   JSONB        NOT NULL DEFAULT '{}',                  -- 元数据

    -- 嵌入列：头像媒体文件（models.MediaFile，embeddedPrefix: avatar_）
    avatar_id                BIGINT       NOT NULL DEFAULT 0,        -- 头像文件雪花ID
    avatar_created_at        BIGINT       NOT NULL DEFAULT 0,
    avatar_updated_at        BIGINT       NOT NULL DEFAULT 0,
    avatar_asset_id          BIGINT       NOT NULL DEFAULT 0,        -- 所属媒体资产ID
    avatar_type              INTEGER      NOT NULL DEFAULT 0,        -- 文件类型(枚举)
    avatar_obj_key           VARCHAR(512) NOT NULL DEFAULT '',       -- s3对象键
    avatar_bucket            VARCHAR(128) NOT NULL DEFAULT '',       -- s3存储桶
    avatar_sha256_sum        VARCHAR(64)  NOT NULL DEFAULT '',       -- SHA256校验和
    avatar_size_bytes        BIGINT       NOT NULL DEFAULT 0,        -- 文件大小(字节)
    avatar_format            INTEGER      NOT NULL DEFAULT 0,        -- 媒体格式(枚举)
    avatar_height            BIGINT       NOT NULL DEFAULT 0,        -- 高度
    avatar_width             BIGINT       NOT NULL DEFAULT 0,        -- 宽度
    avatar_media_meta_data   JSONB        NOT NULL DEFAULT '{}'      -- 元数据
);
