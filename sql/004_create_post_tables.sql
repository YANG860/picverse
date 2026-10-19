-- ============================================================
-- post 相关表
-- 生成自 GORM 模型定义（internal/post/models/models.go）
-- 数据库: PostgreSQL
-- ============================================================

-- -----------------------------------------------------------
-- 1. posts — 帖子表
--    草稿与已发布帖子同表，以 status 区分
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS posts (
    id               BIGINT       NOT NULL PRIMARY KEY,              -- 雪花ID
    created_at       BIGINT       NOT NULL DEFAULT 0,                -- 创建时间(unix毫秒)
    updated_at       BIGINT       NOT NULL DEFAULT 0,                -- 更新时间(unix毫秒)

    author_id        BIGINT       NOT NULL DEFAULT 0,                -- 作者ID(雪花ID)
    title            VARCHAR(256) NOT NULL DEFAULT '',               -- 标题
    full_text        TEXT         NOT NULL DEFAULT '',               -- 正文

    status           INTEGER      NOT NULL DEFAULT 0,                -- 帖子状态(枚举)

    like_count       BIGINT       NOT NULL DEFAULT 0,                -- 点赞数
    favourite_count  BIGINT       NOT NULL DEFAULT 0,                -- 收藏数
    comment_count    BIGINT       NOT NULL DEFAULT 0,                -- 评论数
    view_count       BIGINT       NOT NULL DEFAULT 0,                -- 浏览数

    meta_data        JSONB        NOT NULL DEFAULT '{}'             -- 元数据
);

-- 存量库补列（幂等）：CREATE TABLE IF NOT EXISTS 不会修补已存在的表，
-- 老库同步需单独 ALTER；新库执行为无副作用 no-op
ALTER TABLE posts ADD COLUMN IF NOT EXISTS view_count BIGINT NOT NULL DEFAULT 0;

-- -----------------------------------------------------------
-- 2. post_medias — 帖子与媒体关联表
--    含嵌入的关联媒体文件列（media.MediaFile，前缀 media_）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS post_medias (
    id          BIGINT  NOT NULL PRIMARY KEY,                        -- 雪花ID
    created_at  BIGINT  NOT NULL DEFAULT 0,                          -- 创建时间(unix毫秒)
    updated_at  BIGINT  NOT NULL DEFAULT 0,                          -- 更新时间(unix毫秒)

    post_id     BIGINT  NOT NULL DEFAULT 0,                          -- 所属帖子ID(雪花ID)
    sort_order  INTEGER NOT NULL DEFAULT 0,                          -- 展示顺序

    -- 嵌入列：关联媒体文件（models.MediaFile，embeddedPrefix: media_）
    media_id                BIGINT       NOT NULL DEFAULT 0,         -- 媒体文件雪花ID
    media_created_at        BIGINT       NOT NULL DEFAULT 0,
    media_updated_at        BIGINT       NOT NULL DEFAULT 0,
    media_asset_id          BIGINT       NOT NULL DEFAULT 0,         -- 所属媒体资产ID
    media_type              INTEGER      NOT NULL DEFAULT 0,         -- 文件类型(枚举)
    media_obj_key           VARCHAR(512) NOT NULL DEFAULT '',        -- s3对象键
    media_bucket            VARCHAR(128) NOT NULL DEFAULT '',        -- s3存储桶
    media_sha256_sum        VARCHAR(64)  NOT NULL DEFAULT '',        -- SHA256校验和
    media_size_bytes        BIGINT       NOT NULL DEFAULT 0,         -- 文件大小(字节)
    media_format            INTEGER      NOT NULL DEFAULT 0,         -- 媒体格式(枚举)
    media_height            BIGINT       NOT NULL DEFAULT 0,         -- 高度
    media_width             BIGINT       NOT NULL DEFAULT 0,         -- 宽度
    media_media_meta_data   JSONB        NOT NULL DEFAULT '{}'       -- 元数据
);

-- ============================================================
-- 索引（查询驱动）
-- ============================================================

-- 索引 1: 作者帖子游标分页
-- 对应 repo.GetPostBriefsByAuthor() / repo.GetPublishedPostBriefsByAuthor()
--   WHERE author_id = ? AND created_at < ? [AND status = ?] ORDER BY created_at DESC LIMIT ?
-- author_id 等值过滤 + created_at 范围过滤/排序，复合索引覆盖最左前缀
CREATE INDEX IF NOT EXISTS idx_posts_author_created
    ON posts (author_id, created_at DESC);

-- 索引 2: 按帖子取媒体 / 删除媒体
-- 对应 repo.GetPostMedias() / DeletePostMedias() / GetPostCover()
--   WHERE post_id = ?
CREATE INDEX IF NOT EXISTS idx_post_medias_post_id
    ON post_medias (post_id);
