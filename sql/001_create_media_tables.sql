-- ============================================================
-- media 相关表
-- 生成自 GORM 模型定义（internal/media/models/models.go）
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

-- ============================================================
-- 索引（查询驱动）
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

-- 索引 4: 按资产ID+文件类型联合查询
-- 对应 repo.GetMediaFiles()
--   WHERE (id = ? AND type = ?) OR (id = ? AND type = ?) ...
-- 模型中已有复合索引 idx_media_files_asset_type (asset_id, type)
-- 此处显式创建以确保一致
CREATE INDEX IF NOT EXISTS idx_media_files_asset_type
    ON media_files (asset_id, type);
