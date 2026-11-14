-- ============================================================
-- post 评论表
-- 生成自 GORM 模型定义（internal/post/models/models.go）
-- 数据库: PostgreSQL
--
-- 口径（tmp/comment_plan.md）：
--   - comment_count 以明细为准（status=NORMAL 行数），posts.comment_count 同事务条件更新（强一致）
--   - 两级楼中楼：一级评论 root_id=0（哨兵，proto 层转换为自身 id），回复 root_id=楼层 id、parent_id=被回复评论 id
--   - 软删 status=DELETED；删一级评论级联软删整楼；帖子物理回收（handleDeletePost）时连带物理删除
--   - 不设唯一约束（评论为追加型，与 post_likes 的"一行一态"不同；幂等由软删条件更新保证）
-- ============================================================

-- -----------------------------------------------------------
-- post_comments — 帖子评论（一级评论 + 楼内回复同表）
--    行数口径：posts.comment_count = 该帖 status=NORMAL 的全部行数（一级 + 回复）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS post_comments (
    id               BIGINT       NOT NULL PRIMARY KEY,              -- 雪花ID
    created_at       BIGINT       NOT NULL DEFAULT 0,                -- 创建时间(unix毫秒)
    updated_at       BIGINT       NOT NULL DEFAULT 0,                -- 更新时间(unix毫秒)

    post_id          BIGINT       NOT NULL DEFAULT 0,                -- 所属帖子ID(雪花ID)
    user_id          BIGINT       NOT NULL DEFAULT 0,                -- 评论者ID(雪花ID)

    root_id          BIGINT       NOT NULL DEFAULT 0,                -- 楼层ID(一级评论=0，回复=顶层评论ID)
    parent_id        BIGINT       NOT NULL DEFAULT 0,                -- 被回复评论ID(一级评论=0)
    reply_to_user_id BIGINT       NOT NULL DEFAULT 0,                -- 被回复者ID(一级评论=0)

    content          VARCHAR(512) NOT NULL DEFAULT '',               -- 评论内容(≤500 rune)

    status           INTEGER      NOT NULL DEFAULT 0,                -- 评论状态(枚举：1 NORMAL / 2 DELETED，落库由应用层显式赋值)
    reply_count      BIGINT       NOT NULL DEFAULT 0                 -- 楼层回复数(仅一级评论有意义)
);

-- ============================================================
-- 索引（查询驱动）
-- ============================================================

-- 索引 1: 帖子一级评论游标分页
-- 对应 repo.ListComments()
--   WHERE post_id = ? AND root_id = 0 AND status = 1 AND created_at < ?
--   ORDER BY created_at DESC LIMIT ?
-- post_id + root_id 等值过滤在最左，created_at 范围过滤/排序殿后，keyset 翻页免 filesort
CREATE INDEX IF NOT EXISTS idx_post_comments_post_root
    ON post_comments (post_id, root_id, created_at DESC);

-- 索引 2: 楼内回复游标分页 + 级联软删/计数回收
-- 对应 repo.ListReplies() / DeleteCommentCascade() / CountReplies()
--   WHERE root_id = ? [AND status = 1] [ORDER BY created_at DESC]
CREATE INDEX IF NOT EXISTS idx_post_comments_root
    ON post_comments (root_id, created_at DESC);

-- 索引 3: 用户评论列表（v2 "我的评论" 预留，本版本无消费查询；对齐 007 预留索引惯例）
-- 对应（v2）repo.ListCommentIDsByUser()
--   WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?
CREATE INDEX IF NOT EXISTS idx_post_comments_user
    ON post_comments (user_id, created_at DESC);
