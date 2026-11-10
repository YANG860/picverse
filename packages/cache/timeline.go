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
// per 作者 ZSET：member = post_id、score = created_at，恰为关注流游标二元组 (created_at, id)。
// 窗口语义 = 承诺窗口（fanout.retention，30d）内全量：按时间截尾而非按条数——
// 短取 ⇔ 承诺窗口内真见底（读侧免回源，零回源设计）；条数保险丝仅防事故刷帖。
// key 存在性兼作 pull 分流判据（有缓存 → pull，无缓存 → 收件箱，见 Window）。
// 不做删帖失效（已删帖由下游过滤，与收件箱同口径）；术语注意：勿与消息发件箱 out_box_entries 混淆。
type AuthorTimelineCache interface {
	// Append 发布追加：写入 + 时间截尾（承诺窗口外 member 移除）+ 保险丝 + 刷 TTL
	// （发布链路同步调用，O(log N)）；写失败时发布链路降级 fallback 扇出收件箱
	// （宁重不漏，不允许无供给线）
	Append(ctx context.Context, authorID, postID, createdAt int64) error
	// Window 按游标取窗：严格早于 (beforeTS, beforeID) 的至多 limit 条，(created_at, post_id) 倒序。
	// 分流契约（缓存存在性 = pull 判据）：
	//   - miss = true：该作者未缓存（key 不存在）→ 非 pull 对象，分流到收件箱路
	//     （普通作者/超窗不活跃作者，均不在承诺供给范围）
	//   - miss = false：pull 对象，缓存即承诺窗口内全量，直接参与归并；
	//     短取（len < limit）= 承诺窗口内见底，免回源
	Window(ctx context.Context, authorID, beforeTS, beforeID int64, limit int) (entries []TimelineEntry, miss bool, err error)
	// MarkBigV 大V标识（防抖动）：SET + TTL，已存在覆盖刷新——晋升后至少 mark_ttl 内维持大V
	// 供给路径，粉丝数在阈值附近抖动不切换供给线（发帖缓存连续、不造洞）
	MarkBigV(ctx context.Context, authorID int64) error
	// IsBigV 大V标识判定：标识在（含抖动保持期）→ 仍按大V供给（写缓存不刷新 TTL，自然过期回归扇出）
	IsBigV(ctx context.Context, authorID int64) (bool, error)
}

// AuthorTimelineCacheImpl AuthorTimelineCache 的 Cache 实现：
// key 构造 + ZSET + 时间截尾 + 保险丝 + TTL 抖动（JitterTTL），底层原语经 Cache 注入（Redis/Noop/测试替身）。
type AuthorTimelineCacheImpl struct {
	c         Cache
	retention time.Duration // member 时间截尾窗口 = fanout.retention（承诺窗口）：窗口内全量保留
	capacity  int           // 条数保险丝（防事故刷帖撑爆内存）：正常流量永不触发
	markTTL   time.Duration // 大V标识 TTL（防抖动保持窗）：晋升后至少该时长维持大V供给路径
	ttl       time.Duration
	log       *slog.Logger // 结构性垃圾丢弃留痕（可操作上下文）
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
	var fc config.FanoutConfig
	var bc config.BigVConfig
	if cfg != nil {
		cc = cfg.Cache
		fc = cfg.Fanout
		bc = cfg.BigV
	}
	retention := fc.Retention
	if retention <= 0 {
		retention = defaults.FanoutRetention
	}
	markTTL := bc.MarkTTL
	if markTTL <= 0 {
		markTTL = defaults.BigVMarkTTL
	}
	capacity := cc.TimelineCapacity
	if capacity <= 0 {
		capacity = defaults.CacheTimelineCapacity
	}
	ttl := cc.TimelineTTL
	if ttl <= 0 {
		ttl = defaults.CacheTimelineTTL
	}
	return &AuthorTimelineCacheImpl{c: c, retention: retention, capacity: capacity, markTTL: markTTL, ttl: ttl, log: lf.GetLoggerOf("author_timeline_cache")}
}

// authorTimelineKey 作者发件箱缓存 key（KeyPrefix 的 v1 为序列化 schema 版本）
func authorTimelineKey(id int64) string {
	return idKey(authorTimelinePrefix, id)
}

// authorTimelineID authorTimelineKey 的逆运算（构造/回读同源配对）
func authorTimelineID(key string) (int64, error) {
	return idFromKey(authorTimelinePrefix, key)
}

// authorBigVKey 大V标识 key（防抖动，纯 TTL 哨兵无 value 语义）
func authorBigVKey(id int64) string {
	return idKey(authorBigVPrefix, id)
}

// entryTTL 缓存 TTL（带 ±10% 抖动防雪崩：同批重建 key 同时过期、同时回源）
func (a *AuthorTimelineCacheImpl) entryTTL() time.Duration {
	return JitterTTL(a.ttl, defaults.CacheTTLJitter)
}

// Append 发布追加：ZADD + 时间截尾（承诺窗口外移除）+ 保险丝 + 刷新 TTL
func (a *AuthorTimelineCacheImpl) Append(ctx context.Context, authorID, postID, createdAt int64) error {
	key := authorTimelineKey(authorID)
	if err := a.c.ZAdd(ctx, key, []ZMember{{Score: float64(createdAt), Member: strconv.FormatInt(postID, 10)}}, a.entryTTL()); err != nil {
		return err
	}
	// 时间截尾：删承诺窗口（fanout.retention）外的 member——缓存 = 窗口内全量，
	// 短取 ⇔ 承诺内见底（读侧免回源的零回源设计依据）
	cutoff := float64(time.Now().Add(-a.retention).Unix())
	if err := a.c.ZRemRangeByScore(ctx, key, cutoff, math.Inf(-1)); err != nil {
		return err
	}
	// 保险丝：仅防事故刷帖撑爆内存，正常流量永不触发（触发即极端态，读侧短取见底即可）
	return a.c.ZRemRangeByRank(ctx, key, 0, -(a.capacity + 1))
}

// MarkBigV 大V标识（防抖动）：SET + TTL，已存在覆盖刷新 TTL——晋升后至少 mark_ttl 内维持大V供给路径
func (a *AuthorTimelineCacheImpl) MarkBigV(ctx context.Context, authorID int64) error {
	return a.c.Set(ctx, authorBigVKey(authorID), []byte("1"), a.markTTL)
}

// IsBigV 大V标识判定：标识在（含抖动保持期）→ 仍按大V供给（写缓存不刷新 TTL）
func (a *AuthorTimelineCacheImpl) IsBigV(ctx context.Context, authorID int64) (bool, error) {
	return a.c.Exists(ctx, authorBigVKey(authorID))
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
	// 空结果：区分"未缓存"（miss=true → 分流收件箱路）与"缓存见底/窗内无更早行"（miss=false，
	// 短取 = 承诺窗口内见底，免回源）
	exists, err := a.c.Exists(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return nil, !exists, nil
}
