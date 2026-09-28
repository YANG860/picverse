-- ============================================================
-- auth 相关表
-- ============================================================

-- -----------------------------------------------------------
-- 1. user_credentials — 用户凭证表
--    存储用户注册信息、密码哈希、角色、刷新令牌ID等
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_credentials (
    id                   BIGINT       NOT NULL PRIMARY KEY,              -- 雪花ID
    created_at           BIGINT       NOT NULL DEFAULT 0,                -- 创建时间(unix毫秒)
    updated_at           BIGINT       NOT NULL DEFAULT 0,                -- 更新时间(unix毫秒)

    email                VARCHAR(255) NOT NULL,                          -- 邮箱地址
    password_hash        VARCHAR(255) NOT NULL,                          -- 密码哈希
    role                 INTEGER      NOT NULL DEFAULT 0,                -- 角色(枚举)

    refresh_token_id_web    VARCHAR(255) NOT NULL DEFAULT '',            -- Web端刷新令牌ID
    refresh_token_id_mobile VARCHAR(255) NOT NULL DEFAULT ''             -- Mobile端刷新令牌ID
);

-- 唯一索引：邮箱
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_credentials_email ON user_credentials (email);
