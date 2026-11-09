package defaults

import "time"

// 大V快照默认值（config.bigv 对应项为 0 时回退），见 doc/internal/discover/discover.md
const (
	// BigVSyncInterval 大V快照惰性同步间隔（读触发回源节流，外部 ≈ 稍微滞后的直连 DB）
	BigVSyncInterval = time.Minute
)
