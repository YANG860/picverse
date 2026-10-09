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

-- -----------------------------------------------------------
-- 2. user_follows — 用户关注关系表
--    一行 = 一条关注关系（Fan 关注 Following）
--    following_id / fan_id 均指向 user_profiles.id，
--    建 DB 级外键（fk_user_follows_following / fk_user_follows_fan），
--    用户注销/删除时级联清理关注关系
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_follows (
    id           BIGINT NOT NULL PRIMARY KEY,                        -- 雪花ID
    created_at   BIGINT NOT NULL DEFAULT 0,                          -- 创建时间(unix毫秒)
    updated_at   BIGINT NOT NULL DEFAULT 0,                          -- 更新时间(unix毫秒)

    following_id BIGINT NOT NULL DEFAULT 0,                          -- 关注的用户ID(雪花ID)
    fan_id       BIGINT NOT NULL DEFAULT 0,                          -- 粉丝ID(雪花ID)

    -- 防自关注（fan_id 必须不同于 following_id）
    CONSTRAINT chk_user_follows_no_self CHECK (fan_id <> following_id),

    -- 外键：被关注用户 → user_profiles.id
    CONSTRAINT fk_user_follows_following FOREIGN KEY (following_id)
        REFERENCES user_profiles (id) ON UPDATE CASCADE ON DELETE CASCADE,
    -- 外键：粉丝用户 → user_profiles.id
    CONSTRAINT fk_user_follows_fan FOREIGN KEY (fan_id)
        REFERENCES user_profiles (id) ON UPDATE CASCADE ON DELETE CASCADE
);

-- 外键约束兜底（已有库）：CREATE TABLE IF NOT EXISTS 不会为已存在的表追加约束，
-- 以下幂等语句（DROP IF EXISTS 后重建）为已有库补建外键；新建库下重建同名约束，无副作用。
-- 前提：following_id / fan_id 无孤儿数据（均存在于 user_profiles.id），否则 ADD CONSTRAINT 失败。
-- 注意：ON DELETE CASCADE 只清理关注关系行，不回写 user_profiles.followers/followings 计数，
--       计数由应用层维护。
ALTER TABLE user_follows DROP CONSTRAINT IF EXISTS fk_user_follows_following;
ALTER TABLE user_follows ADD CONSTRAINT fk_user_follows_following
    FOREIGN KEY (following_id) REFERENCES user_profiles (id)
    ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE user_follows DROP CONSTRAINT IF EXISTS fk_user_follows_fan;
ALTER TABLE user_follows ADD CONSTRAINT fk_user_follows_fan
    FOREIGN KEY (fan_id) REFERENCES user_profiles (id)
    ON UPDATE CASCADE ON DELETE CASCADE;

-- ============================================================
-- 索引（查询驱动）
-- ============================================================

-- 索引 1: 防止重复关注 / 判重 / 取关 / 关注列表
-- 对应查询:
--   WHERE fan_id = ? AND following_id = ?   -- 判重、取关（唯一约束精确匹配）
--   WHERE fan_id = ?                        -- "我关注的人"列表（最左前缀覆盖）
-- 复合唯一索引同时承担唯一约束，从 DB 层杜绝重复关注
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_follows_fan_following
    ON user_follows (fan_id, following_id);

-- 索引 2: 粉丝列表
-- 对应查询:
--   WHERE following_id = ?                  -- "我的粉丝"列表
-- 复合唯一索引最左前缀为 fan_id，无法覆盖反向查询，需单列索引
CREATE INDEX IF NOT EXISTS idx_user_follows_following_id
    ON user_follows (following_id);
