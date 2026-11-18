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
	// CacheTimelineCapacity 作者发件箱缓存条数保险丝默认值（防事故刷帖撑爆内存）。
	// 正常流量永不触发：窗口由时间截尾（fanout.retention）控制，30d 内全量保留
	CacheTimelineCapacity = 5000
	// CacheTTLJitter TTL 抖动比例（±10%，防雪崩）
	CacheTTLJitter = 0.1
	// CacheFlightWorkers 回源合并并发批上限默认值（BatchFlight K-worker）
	CacheFlightWorkers = 4
	// CacheFlightBatchSize 回源合并单批 key 上限默认值（积压自动切分多批）
	CacheFlightBatchSize = 500
	// CacheFlightTimeout 回源合并单批 fetch 超时默认值（防挂死毒化底线）
	CacheFlightTimeout = 5 * time.Second
	// CacheStatTTL 统计列缓存 TTL 默认值（短 TTL 读缓存：计数高频变更，有界陈旧）
	CacheStatTTL = time.Minute
)
