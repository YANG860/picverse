package defaults

import "time"

var (
	// CacheProfileTTL 用户资料缓存 TTL 默认值（配置未指定时使用）
	CacheProfileTTL = 30 * time.Minute
	// CachePostTTL 帖子详情缓存 TTL 默认值（主体发布后近似不变，与资料同档）
	CachePostTTL = 30 * time.Minute
	// CacheNullTTL 空哨兵 TTL 默认值（防穿透；短 TTL 保证不存在的用户尽快可见）
	CacheNullTTL = time.Minute
	// CacheTimelineTTL 作者发件箱缓存 TTL 默认值（30 天：与 fanout.retention 对齐——
	// 缓存 key 存活 ≥ 翻页可见寿命是零漏前提，改任一侧须同步对齐，见 doc/internal/discover/discover.md）
	CacheTimelineTTL = 30 * 24 * time.Hour
	// CacheTimelineCapacity 作者发件箱缓存每作者近期帖窗口默认条数（pull 现拉页大小远小于此）
	CacheTimelineCapacity = 200
	// CacheTTLJitter TTL 抖动比例（±10%，防雪崩）
	CacheTTLJitter = 0.1
)
