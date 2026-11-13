package defaults

import "time"

// 推荐流（GetRecommendFeed）默认参数（config.RecommendConfig 为 0 时回退）
const (
	// RecommendSeenTTL seen 已曝光去重窗口（也是账本 key TTL，随记账滚动续期）
	RecommendSeenTTL = 720 * time.Hour
	// RecommendScanBatchFactor 候选扫描批次 = limit × factor（预留 seen/打散过滤跳过）
	RecommendScanBatchFactor = 3
	// RecommendScatterWindow 同作者打散滑窗（窗内同作者 ≤ 1）
	RecommendScatterWindow = 3
	// RecommendExploreRatio 探索位比例（每页混入的随机采样条数占比，至少 1 条）
	RecommendExploreRatio = 0.1
)
