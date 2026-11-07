package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"rednote/config"
	post "rednote/internal/post/models"
	"rednote/packages/defaults"
	"rednote/packages/logger"
)

// PostEntityCache 帖子实体缓存（cache-aside 存取层）：以 post_id 为粒度缓存帖子实体，
// 与 UserEntityCache 同一定式——主体（非计数列，含状态与媒体关联）与统计列分块存储。
//
// 读语义（"nil 与 空"双标记）：
//   - id 不在返回 map 中：miss（未缓存），调用方回源后用 Store* 回填；
//   - map[id] == nil：空哨兵命中，确认该帖子不存在（无需回源，防穿透）；
//   - map[id] != nil：命中缓存实体。
//
// 写语义：Store*(ctx, id, nil) 写入空哨兵（缓存"查询结果为空"）。
// 失效语义：Del* 删除对应实体 key（幂等，缺失静默跳过），供写后失效登记。
// 错误一律上抛由调用方降级（fail-open），不在本层吞错。
type PostEntityCache interface {
	// LoadPostDetails 批量读帖子详情（非计数列：含状态与媒体关联；计数列为零值，
	// 调用方以 GetPostStat 轻查覆盖）
	LoadPostDetails(ctx context.Context, ids []int64) (map[int64]*post.PostDetail, error)
	// StorePostDetail 写帖子详情（只存非计数列）；detail == nil 时写空哨兵
	StorePostDetail(ctx context.Context, id int64, detail *post.PostDetail) error
	// DelPostDetails 批量删除帖子详情缓存（写后失效）
	DelPostDetails(ctx context.Context, ids []int64) error

	// ---- 统计列（就绪待启用：统计轻查成为热点后升级写回式缓存，对称 user 的 stat API）----

	// LoadPostStats 批量读帖子统计列
	LoadPostStats(ctx context.Context, ids []int64) (map[int64]*post.PostStat, error)
	// StorePostStat 写帖子统计列；stat == nil 时写空哨兵
	StorePostStat(ctx context.Context, id int64, stat *post.PostStat) error
	// DelPostStats 批量删除帖子统计列缓存
	DelPostStats(ctx context.Context, ids []int64) error

	// ---- 浏览计数器（PV 近似，只丢不重）----

	// IncrPostViewCount 浏览计数 +1（PV 近似，调用方 fire-and-forget 降级）。
	// 计数累加在独立计数器 key（不与缓存实体混写，避免缓存失效误伤计数）
	IncrPostViewCount(ctx context.Context, id int64) error
	// TakePostViewDeltas 原子取走全部待刷回的浏览增量（id → delta，取走即清除，只丢不重）；
	// 非法 key/值（结构性垃圾，正常写入路径不存在）取走即弃并留 Warn
	TakePostViewDeltas(ctx context.Context) (map[int64]int64, error)
}

// PostEntityCacheImpl PostEntityCache 的 Cache 实现：key 构造 + JSON 序列化 +
// 空哨兵（NullSentinel）+ TTL 抖动（JitterTTL），底层原语经 Cache 注入（Redis/Noop/测试替身）。
type PostEntityCacheImpl struct {
	c   Cache
	cfg config.CacheConfig // TTL 来源：字段 > 0 取配置，否则回退 defaults
	log *slog.Logger       // 结构性垃圾丢弃留痕（可操作上下文）
}

var _ PostEntityCache = (*PostEntityCacheImpl)(nil)

// NewPostEntityCache 创建帖子实体缓存。cfg 可为 nil（全部 TTL 回退 defaults）；
// cfg 字段为 0 时同样回退 defaults（"config 其次 defaults"）。lf 用于垃圾丢弃 Warn 留痕。
func NewPostEntityCache(c Cache, cfg *config.Config, lf logger.LoggerFactory) *PostEntityCacheImpl {
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
	return &PostEntityCacheImpl{c: c, cfg: cc, log: lf.GetLoggerOf("post_cache")}
}

// ==================== key 与 TTL ====================

// postDetailKey 帖子详情缓存 key（KeyPrefix 的 v1 为序列化 schema 版本）
func postDetailKey(id int64) string {
	return idKey(postDetailPrefix, id)
}

// postStatKey 帖子统计列缓存 key
func postStatKey(id int64) string {
	return idKey(postStatPrefix, id)
}

// postViewKey 浏览计数器 key（独立于实体缓存 key 空间，TTL 到期自动清空）
func postViewKey(id int64) string {
	return idKey(postViewPrefix, id)
}

// postViewID postViewKey 的逆运算（构造/回读同源配对）
func postViewID(key string) (int64, error) {
	return idFromKey(postViewPrefix, key)
}

// viewCounterTTL 计数器 TTL 安全网：正常每刷回周期（默认 30s）即被取走，
// TTL 只防刷回长期停摆时计数器无界堆积（过期丢失符合近似口径），每次 +1 刷新
const viewCounterTTL = 24 * time.Hour

// detailTTL 详情缓存 TTL：config 其次 defaults
func (u *PostEntityCacheImpl) detailTTL() time.Duration {
	if u.cfg.PostTTL > 0 {
		return u.cfg.PostTTL
	}
	return defaults.CachePostTTL
}

// statTTL 统计列缓存 TTL：config 暂无 StatTTL 字段，用短 TTL 常量兜底（就绪待启用）
func (u *PostEntityCacheImpl) statTTL() time.Duration {
	return defaultStatTTL
}

// nullTTL 空哨兵 TTL：config 其次 defaults
func (u *PostEntityCacheImpl) nullTTL() time.Duration {
	if u.cfg.NullTTL > 0 {
		return u.cfg.NullTTL
	}
	return defaults.CacheNullTTL
}

// ==================== 详情主体（非计数列） ====================

// LoadPostDetails 批量读帖子详情缓存（MGET 对齐，逐 id 判定 miss/哨兵/命中）。
// 媒体关联（Medias）随实体整体 JSON 往返（数组保序，sort_order 不丢）。
// 序列化损坏按错误上抛（调用方降级全量回源并回填，自愈）。
func (u *PostEntityCacheImpl) LoadPostDetails(ctx context.Context, ids []int64) (map[int64]*post.PostDetail, error) {
	vals, err := MGetKeys(ctx, u.c, ids, postDetailKey)
	if err != nil {
		return nil, fmt.Errorf("cache post detail mget: %w", err)
	}
	details := make(map[int64]*post.PostDetail, len(ids))
	for i, id := range ids {
		val := vals[i]
		if val == nil {
			continue // miss：不入 map，调用方回源
		}
		if IsNullSentinel(val) {
			details[id] = nil // 空哨兵：确认不存在
			continue
		}
		var p post.PostDetail
		if err := json.Unmarshal(val, &p); err != nil {
			return nil, fmt.Errorf("cache post detail unmarshal %d: %w", id, err)
		}
		details[id] = &p
	}
	return details, nil
}

// StorePostDetail 写帖子详情缓存：只存非计数列（4 个计数清零后入缓存，统计列由
// GetPostStat 独立提供——互动事件高频写因此完全不触碰缓存，旧缓存残留计数也无害）。
// 状态（Status）与媒体关联（Medias）随实体入缓存：状态供可见性裁决、媒体供组装一次拿全。
// 写入 TTL 附带 ±10% 抖动防雪崩；detail == nil 写空哨兵（短 TTL 防穿透）。id 为 key 依据。
func (u *PostEntityCacheImpl) StorePostDetail(ctx context.Context, id int64, detail *post.PostDetail) error {
	key := postDetailKey(id)
	if detail == nil {
		if err := u.c.Set(ctx, key, NullSentinel, u.nullTTL()); err != nil {
			return fmt.Errorf("cache post detail set null sentinel %d: %w", id, err)
		}
		return nil
	}
	cached := *detail // 拷贝：不改动调用方实体
	cached.LikeCount = 0
	cached.FavouriteCount = 0
	cached.CommentCount = 0
	cached.ViewCount = 0
	val, err := json.Marshal(&cached)
	if err != nil {
		return fmt.Errorf("cache post detail marshal %d: %w", id, err)
	}
	if err := u.c.Set(ctx, key, val, JitterTTL(u.detailTTL(), defaults.CacheTTLJitter)); err != nil {
		return fmt.Errorf("cache post detail set %d: %w", id, err)
	}
	return nil
}

// DelPostDetails 批量删除帖子详情缓存（写后失效：DEL 对应 key，幂等）。
// 失效失败错误上抛，调用方记 warn 后靠 TTL 兜底即可（陈旧有界），不阻断业务。
func (u *PostEntityCacheImpl) DelPostDetails(ctx context.Context, ids []int64) error {
	if err := DelKeys(ctx, u.c, ids, postDetailKey); err != nil {
		return fmt.Errorf("cache post detail del: %w", err)
	}
	return nil
}

// ==================== 统计列（就绪待启用） ====================

// LoadPostStats 批量读帖子统计列缓存，map 语义同 LoadPostDetails（nil 与 空双标记）。
func (u *PostEntityCacheImpl) LoadPostStats(ctx context.Context, ids []int64) (map[int64]*post.PostStat, error) {
	vals, err := MGetKeys(ctx, u.c, ids, postStatKey)
	if err != nil {
		return nil, fmt.Errorf("cache post stat mget: %w", err)
	}
	stats := make(map[int64]*post.PostStat, len(ids))
	for i, id := range ids {
		val := vals[i]
		if val == nil {
			continue // miss：不入 map，调用方回源
		}
		if IsNullSentinel(val) {
			stats[id] = nil // 空哨兵：确认不存在
			continue
		}
		var s post.PostStat
		if err := json.Unmarshal(val, &s); err != nil {
			return nil, fmt.Errorf("cache post stat unmarshal %d: %w", id, err)
		}
		stats[id] = &s
	}
	return stats, nil
}

// StorePostStat 写帖子统计列缓存（短 TTL + 抖动，计数高频变更保证有界陈旧）；
// stat == nil 写空哨兵。id 为 key 依据。
func (u *PostEntityCacheImpl) StorePostStat(ctx context.Context, id int64, stat *post.PostStat) error {
	key := postStatKey(id)
	if stat == nil {
		if err := u.c.Set(ctx, key, NullSentinel, u.nullTTL()); err != nil {
			return fmt.Errorf("cache post stat set null sentinel %d: %w", id, err)
		}
		return nil
	}
	val, err := json.Marshal(stat)
	if err != nil {
		return fmt.Errorf("cache post stat marshal %d: %w", id, err)
	}
	if err := u.c.Set(ctx, key, val, JitterTTL(u.statTTL(), defaults.CacheTTLJitter)); err != nil {
		return fmt.Errorf("cache post stat set %d: %w", id, err)
	}
	return nil
}

// DelPostStats 批量删除帖子统计列缓存（写后失效：DEL 对应 key，幂等）。
func (u *PostEntityCacheImpl) DelPostStats(ctx context.Context, ids []int64) error {
	if err := DelKeys(ctx, u.c, ids, postStatKey); err != nil {
		return fmt.Errorf("cache post stat del: %w", err)
	}
	return nil
}

// ==================== 浏览计数器（PV 近似，只丢不重） ====================

// IncrPostViewCount 浏览计数 +1（PV 近似，调用方 fire-and-forget 降级，失败不影响主流程）。
// 计数累加在独立计数器 key（不与缓存实体混写，避免缓存失效误伤计数）；
// TTL 为安全网（viewCounterTTL），每次 +1 刷新。
func (u *PostEntityCacheImpl) IncrPostViewCount(ctx context.Context, id int64) error {
	if err := u.c.IncrCounter(ctx, postViewKey(id), viewCounterTTL); err != nil {
		return fmt.Errorf("cache incr post view %d: %w", id, err)
	}
	return nil
}

// TakePostViewDeltas 原子取走全部待刷回的浏览增量（id → delta，取走即清除，只丢不重）。
// 非法 key/值（结构性垃圾，正常写入路径不存在）取走即弃并留 Warn（不静默吞掉）。
func (u *PostEntityCacheImpl) TakePostViewDeltas(ctx context.Context) (map[int64]int64, error) {
	vals, err := u.c.TakeCounters(ctx, postViewPrefix)
	if err != nil {
		return nil, fmt.Errorf("cache take post view deltas: %w", err)
	}
	deltas := make(map[int64]int64, len(vals))
	for key, val := range vals {
		id, err := postViewID(key)
		if err != nil {
			u.log.Warn("view counter dropped: invalid key", "key", key)
			continue
		}
		delta, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			u.log.Warn("view counter dropped: invalid value", "key", key, "value", val)
			continue
		}
		deltas[id] = delta
	}
	return deltas, nil
}
