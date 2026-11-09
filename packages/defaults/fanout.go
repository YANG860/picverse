package defaults

import "time"

// 收件箱（fanout_timelines）保留期清理默认参数（config.FanoutConfig 为 0 时回退）
const (
	// FanoutRetention 收件箱默认保留期（按帖子时间 post_created_at 口径，30 天）
	FanoutRetention = 30 * 24 * time.Hour
	// FanoutCleanupInterval 清理任务默认运行周期（1 小时）
	FanoutCleanupInterval = time.Hour
	// FanoutCleanupBatch 单条 DELETE 的批大小（ctid 批删，批间独立提交防长锁）
	FanoutCleanupBatch = 10000
)
