-- ============================================================
-- post 互动相关表（点赞 / 收藏明细）
-- 生成自 GORM 模型定义（internal/post/models/models.go）
-- 数据库: PostgreSQL
--
-- 口径（doc/design/interaction.md）：
--   - like_count / favourite_count 以明细为准，posts 统计列同事务条件更新（强一致）
--   - 浏览计数无明细表（PV 近似，异步消息累加 posts.view_count），故本文件不含 post_views
--   - 明细表唯一约束 (post_id, user_id) + OnConflict Do Nothing 天然幂等
-- ============================================================

-- -----------------------------------------------------------
-- 1. post_likes — 帖子点赞明细
--    一行 = 一个用户对一个帖子的点赞（明细为准，like_count = 行数）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS post_likes (
    id         BIGINT NOT NULL PRIMARY KEY,                          -- 雪花ID
    created_at BIGINT NOT NULL DEFAULT 0,                            -- 创建时间(unix毫秒)
    updated_at BIGINT NOT NULL DEFAULT 0,                            -- 更新时间(unix毫秒)

    post_id    BIGINT NOT NULL DEFAULT 0,                            -- 帖子ID(雪花ID)
    user_id    BIGINT NOT NULL DEFAULT 0                             -- 点赞者ID(雪花ID)
);

-- 防重复点赞（OnConflict Do Nothing 幂等锚点）：一行 = 一赞，行数即 like_count
ALTER TABLE post_likes
    ADD CONSTRAINT uk_post_likes_post_user UNIQUE (post_id, user_id);

-- -----------------------------------------------------------
-- 2. post_favourites — 帖子收藏明细
--    与 post_likes 同构（明细为准，favourite_count = 行数）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS post_favourites (
    id         BIGINT NOT NULL PRIMARY KEY,                          -- 雪花ID
    created_at BIGINT NOT NULL DEFAULT 0,                            -- 创建时间(unix毫秒)
    updated_at BIGINT NOT NULL DEFAULT 0,                            -- 更新时间(unix毫秒)

    post_id    BIGINT NOT NULL DEFAULT 0,                            -- 帖子ID(雪花ID)
    user_id    BIGINT NOT NULL DEFAULT 0                             -- 收藏者ID(雪花ID)
);

-- 防重复收藏（OnConflict Do Nothing 幂等锚点）
ALTER TABLE post_favourites
    ADD CONSTRAINT uk_post_favourites_post_user UNIQUE (post_id, user_id);

-- ============================================================
-- 索引（查询驱动）
-- ============================================================

-- 索引 1: 用户点赞列表（v2 "我赞过的帖子" 预留，本版本无消费查询）
-- 对应（v2）repo.GetLikedPostIDs()
--   WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?
-- 唯一约束 uk_post_likes_post_user 最左列为 post_id，服务不了按 user 的反查，需独立索引
CREATE INDEX IF NOT EXISTS idx_post_likes_user
    ON post_likes (user_id, created_at DESC);

-- 索引 2: 用户收藏列表（v2 "我收藏的帖子" 预留，本版本无消费查询）
-- 对应（v2）repo.GetFavouritedPostIDs()
--   WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?
CREATE INDEX IF NOT EXISTS idx_post_favourites_user
    ON post_favourites (user_id, created_at DESC);
