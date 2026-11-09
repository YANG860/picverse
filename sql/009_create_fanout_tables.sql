-- ============================================================
-- 关注流扇出收件箱（推拉结合的 push 路径）
-- 生成自 GORM 模型定义（internal/discover/models/models.go）
-- 数据库: PostgreSQL
--
-- 口径（doc/internal/discover/discover.md）：
--   - 一行 = 一名粉丝收件箱里的一条帖子引用：
--     名单外作者发帖 → 扇出写入其粉丝收件箱（名单内大V由 pull 现拉，不写扩散）
--   - 游标与 pull 路径共享 (post_created_at, post_id)（即帖子的 (created_at, id)），
--     2 路归并按 post_id 去重（宁重不漏）
--   - 幂等：复合主键 (uid, post_id)，扇出消费 at-least-once 重投安全（ON CONFLICT DO NOTHING）
--   - 不级联删除：已删帖不清理行（由下游 BFF 回查组装与前端自行过滤），
--     行随保留期按 post_created_at 时间窗批量删回收（策略待定）
-- ============================================================

-- -----------------------------------------------------------
-- 1. fanout_timelines — 收件箱表
--    一行 = 一名粉丝收件箱里的一条帖子引用（写扩散，行数 = 粉丝数 x 发帖数，需保留期清理）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS fanout_timelines (
    uid             BIGINT  NOT NULL,               -- 收件人(粉丝)ID(雪花)
    post_id         BIGINT  NOT NULL,               -- 帖子ID(雪花)
    author_id       BIGINT  NOT NULL DEFAULT 0,     -- 作者ID(冗余：排查/运维辅助)
    post_created_at BIGINT  NOT NULL DEFAULT 0,     -- 帖子创建时刻(unix毫秒)，游标分量（与 pull 路径共享）
    created_at      BIGINT  NOT NULL DEFAULT 0,     -- 创建时间(unix毫秒)
    updated_at      BIGINT  NOT NULL DEFAULT 0,     -- 更新时间(unix毫秒)
    PRIMARY KEY (uid, post_id)                      -- 复合主键 = 幂等：扇出重投 ON CONFLICT DO NOTHING
);

-- 收件箱 keyset 分页（GetFollowingFeed push 路径）：
-- WHERE uid = ? AND (post_created_at < ? OR (post_created_at = ? AND post_id < ?))
-- ORDER BY post_created_at DESC, post_id DESC LIMIT ?
CREATE INDEX IF NOT EXISTS idx_fanout_timelines_uid_cursor ON fanout_timelines (uid, post_created_at DESC, post_id DESC);

-- 保留期清理：按帖子时间窗批量删（WHERE post_created_at < ?，策略待定）
-- （不做删帖级联删除，无按 post_id 删除路径，故不建 post_id 索引）
CREATE INDEX IF NOT EXISTS idx_fanout_timelines_post_created_at ON fanout_timelines (post_created_at);
