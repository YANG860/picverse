-- ============================================================
-- discover 相关表
-- 生成自 GORM 模型定义（internal/discover/models/models.go）
-- 数据库: PostgreSQL
--
-- 口径（doc/internal/discover/discover.md）：
--   - discover_bigv_authors 推拉结合的大V判定口径（单一事实源）：
--     行存在 = 在 pull 现拉名单（现任大V + 宽限期降级者，发帖不 fanout 扩散、由 pull 现拉）
--   - 降级写 demoted_at 并保留宽限期供 pull 兜底防漏帖，宽限过期物理删；
--     宽限行复用晋升时重置 demoted_at = NULL
--   - 表维护（晋升/降级/清理）在发布帖子时判定（P1，随 fanout 链路落地）；
--     读侧为惰性内存镜像（internal/discover/v1/repo.go，≈ 稍微滞后的无感直连 DB）
-- ============================================================

-- -----------------------------------------------------------
-- 1. discover_bigv_authors — 大V快照表
--    一行 = 一个当前/宽限期内的大V（uid 主键，非雪花自增；表规模 = 大V占比）
-- -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS discover_bigv_authors (
    uid            BIGINT  NOT NULL PRIMARY KEY,               -- 用户ID(雪花)，无自增主键
    became_bigv_at BIGINT  NOT NULL DEFAULT 0,                 -- 本次晋升时刻(unix毫秒)
    demoted_at     BIGINT  DEFAULT NULL,                       -- 降级时刻(unix毫秒)；NULL=现任大V
    followers      BIGINT  NOT NULL DEFAULT 0,                 -- 判定时粉丝数快照（审计/调参）
    created_at     BIGINT  NOT NULL DEFAULT 0,                 -- 创建时间(unix毫秒)
    updated_at     BIGINT  NOT NULL DEFAULT 0                  -- 更新时间(unix毫秒)
);

-- 宽限过期清理（demoted_at < cutoff）走此索引；表极小，全量扫描亦可，保留省清理开销
CREATE INDEX IF NOT EXISTS idx_discover_bigv_demoted_at ON discover_bigv_authors (demoted_at);
