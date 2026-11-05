package defaults

import "time"

var (
	// CacheProfileTTL 用户资料缓存 TTL 默认值（配置未指定时使用）
	CacheProfileTTL = 30 * time.Minute
	// CacheNullTTL 空哨兵 TTL 默认值（防穿透；短 TTL 保证不存在的用户尽快可见）
	CacheNullTTL = time.Minute
	// CacheTTLJitter TTL 抖动比例（±10%，防雪崩）
	CacheTTLJitter = 0.1
)
