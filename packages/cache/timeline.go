package cache

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"time"

	"rednote/config"
	"rednote/packages/defaults"
	"rednote/packages/logger"
)

// TimelineEntry 发件箱缓存条目 = 关注流游标二元组 (created_at, post_id)
type TimelineEntry struct {
	PostID    int64
	CreatedAt int64
}

// AuthorTimelineCache 作者发件箱（帖子时间线）缓存：pull 现拉的加速层（方案 A：统一缓存）。
// per 作者 ZSET 窗口：member = post_id、score = created_at，恰为关注流游标二元组 (created_at, id)；
// 只保近期 Capacity 条（最新连续后缀），TTL 自然回收。
// 语义边界（宁重不漏）：DB 仍是事实源，缓存只是加速——短取（返回 < limit）不等于"到底"；
// key 存在性兼作 pull 分流判据（有缓存 → pull，无缓存 → 收件箱，见 Window）。
// 不做删帖失效（已删帖由下游过滤，与收件箱同口径）；术语注意：勿与消息发件箱 out_box_entries 混淆。
type AuthorTimelineCache interface {
	// Append 发布追加：写入并截尾保留近 Capacity 条（发布链路同步调用，O(log N)）；
	// 写失败时发布链路降级 fallback 扇出收件箱（宁重不漏，不允许无供给线）
	Append(ctx context.Context, authorID, postID, createdAt int64) error
	// Window 按游标取窗：严格早于 (beforeTS, beforeID) 的至多 limit 条，(created_at, post_id) 倒序。
	// 分流契约（缓存存在性 = pull 判据）：
	//   - miss = true：该作者未缓存（key 不存在）→ 非 pull 对象，分流到收件箱路（不回源，普通作者零 DB 开销）
	//   - miss = false 且 len(entries) < limit：pull 对象但缓存不能完整作答（见底/同刻游标过滤）→
	//     调用方以 DB 补足并 Rebuild（pull 对象是大V，回源量小）；短取不等于到底
	Window(ctx context.Context, authorID, beforeTS, beforeID int64, limit int) (entries []TimelineEntry, miss bool, err error)
	// Rebuild 回源回填：以 DB 行并入缓存并截尾（增量 ZADD 不 DEL，与并发 Append 天然合并）。
	// entries 必须是该作者"最新的至多 Capacity 条"（DB 按 (created_at, id) 倒序 LIMIT 回源行），
	// 维持"缓存 = 最新连续后缀"不变式——严禁用深翻页窗口行回填（制造空洞会把短取误判为完整而漏帖）
	Rebuild(ctx context.Context, authorID int64, entries []TimelineEntry) error
}

// AuthorTimelineCacheImpl AuthorTimelineCache 的 Cache 实现：
// key 构造 + ZSET 窗口 + 截尾 + TTL 抖动（JitterTTL），底层原语经 Cache 注入（Redis/Noop/测试替身）。
type AuthorTimelineCacheImpl struct {
	c        Cache
	capacity int
	ttl      time.Duration
	log      *slog.Logger // 结构性垃圾丢弃留痕（可操作上下文）
}

var _ AuthorTimelineCache = (*AuthorTimelineCacheImpl)(nil)

// windowSlack 同刻游标过滤的富余量：游标边界同毫秒多帖被过滤后仍能填满页
const windowSlack = 64

// NewAuthorTimelineCache 创建发件箱缓存。cfg 可为 nil（全部参数回退 defaults）；
// cfg 字段为 0 时同样回退 defaults（"config 其次 defaults"）。
func NewAuthorTimelineCache(c Cache, cfg *config.Config, lf logger.LoggerFactory) *AuthorTimelineCacheImpl {
	if c == nil {
		panic(fmt.Errorf("cache: cache client is required"))
	}
	if lf == nil {
		panic(fmt.Errorf("cache: logger factory is required"))
	}
	var cc config.CacheConfig
	if cfg != nil {
		cc = cfg.Cache
	}
	capacity := cc.TimelineCapacity
	if capacity <= 0 {
		capacity = defaults.CacheTimelineCapacity
	}
	ttl := cc.TimelineTTL
	if ttl <= 0 {
		ttl = defaults.CacheTimelineTTL
	}
	return &AuthorTimelineCacheImpl{c: c, capacity: capacity, ttl: ttl, log: lf.GetLoggerOf("author_timeline_cache")}
}

// authorTimelineKey 作者发件箱缓存 key（KeyPrefix 的 v1 为序列化 schema 版本）
func authorTimelineKey(id int64) string {
	return idKey(authorTimelinePrefix, id)
}

// authorTimelineID authorTimelineKey 的逆运算（构造/回读同源配对）
func authorTimelineID(key string) (int64, error) {
	return idFromKey(authorTimelinePrefix, key)
}

// entryTTL 缓存 TTL（带 ±10% 抖动防雪崩：同批重建 key 同时过期、同时回源）
func (a *AuthorTimelineCacheImpl) entryTTL() time.Duration {
	return JitterTTL(a.ttl, defaults.CacheTTLJitter)
}

// Append 发布追加：ZADD + 截尾（保最新 capacity 条）+ 刷新 TTL
func (a *AuthorTimelineCacheImpl) Append(ctx context.Context, authorID, postID, createdAt int64) error {
	return a.add(ctx, authorID, []TimelineEntry{{PostID: postID, CreatedAt: createdAt}})
}

// Rebuild 回源回填：增量并入（不 DEL，与并发 Append 天然合并）+ 截尾
func (a *AuthorTimelineCacheImpl) Rebuild(ctx context.Context, authorID int64, entries []TimelineEntry) error {
	if len(entries) == 0 {
		return nil
	}
	return a.add(ctx, authorID, entries)
}

// add 写入并截尾（Append/Rebuild 共用）
func (a *AuthorTimelineCacheImpl) add(ctx context.Context, authorID int64, entries []TimelineEntry) error {
	key := authorTimelineKey(authorID)
	members := make([]ZMember, len(entries))
	for i, e := range entries {
		members[i] = ZMember{Score: float64(e.CreatedAt), Member: strconv.FormatInt(e.PostID, 10)}
	}
	if err := a.c.ZAdd(ctx, key, members, a.entryTTL()); err != nil {
		return err
	}
	// 截尾保最新 capacity 条（排名按 score 升序：0..-(capacity+1) 为旧的 capacity 条之外……
	// 固定删最旧尾部：保最新 = 删排名 [0, -(capacity+1)]）
	return a.c.ZRemRangeByRank(ctx, key, 0, -(a.capacity + 1))
}

// Window 按游标取窗（严格早于 (beforeTS, beforeID)，(created_at, post_id) 倒序）。
// 取数走 [min=−inf, max=beforeTS] 降序 + 富余量，游标元组过滤在本层做（同刻按 post_id 严格序），
// 返回前按元组精确重排（redis 同 score 为 member 字典序，≠ 数值序）。
// 空结果再查 Exists 区分 miss（未缓存）与"缓存见底"（key 在但窗内无更早行）
func (a *AuthorTimelineCacheImpl) Window(ctx context.Context, authorID, beforeTS, beforeID int64, limit int) ([]TimelineEntry, bool, error) {
	if limit <= 0 {
		limit = a.capacity
	}
	key := authorTimelineKey(authorID)

	maxScore := math.Inf(1)
	if beforeTS > 0 {
		maxScore = float64(beforeTS)
	}
	fetched, err := a.c.ZRevRangeByScore(ctx, key, maxScore, math.Inf(-1), limit+windowSlack)
	if err != nil {
		return nil, false, err
	}

	entries := make([]TimelineEntry, 0, len(fetched))
	for _, z := range fetched {
		postID, err := strconv.ParseInt(z.Member, 10, 64)
		if err != nil {
			// 结构性垃圾（正常写入路径不存在）取走即弃并留痕
			a.log.Warn("timeline cache: drop malformed member", "key", key, "member", z.Member, "error", err)
			continue
		}
		createdAt := int64(z.Score)
		// 严格早于游标 (beforeTS, beforeID)（(created_at, post_id) 元组序；游标为零 = 首页）
		if beforeTS > 0 || beforeID > 0 {
			if createdAt > beforeTS || (createdAt == beforeTS && postID >= beforeID) {
				continue
			}
		}
		entries = append(entries, TimelineEntry{PostID: postID, CreatedAt: createdAt})
	}
	// (created_at, post_id) 倒序精确重排（member 字典序 ≠ 数值序）
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt != entries[j].CreatedAt {
			return entries[i].CreatedAt > entries[j].CreatedAt
		}
		return entries[i].PostID > entries[j].PostID
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}

	if len(entries) > 0 {
		return entries, false, nil
	}
	// 空结果：区分"未缓存"（miss → 回源重建）与"缓存见底/窗内无更早行"（miss=false，
	// 短取契约：len < limit 即调用方补 DB，宁重不漏）
	exists, err := a.c.Exists(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return nil, !exists, nil
}
