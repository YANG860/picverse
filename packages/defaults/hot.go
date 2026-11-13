package defaults

import "time"

// 热榜池（rn:v1:hot:posts）维护任务与热度分默认参数（config.HotConfig 为 0 时回退）
const (
	// HotRefreshInterval 维护周期（入池/算分/淘汰/TTL 续期一步完成）
	HotRefreshInterval = 10 * time.Minute
	// HotWindow 候选时间窗（7 天；窗口外帖子自然出池）
	HotWindow = 168 * time.Hour
	// HotTTL 池 key TTL 兜底（3 × 周期：维护停摆时池自动消亡，热榜/推荐流降级空页）；
	// 随任何写入（维护重建/发帖钩子）刷新
	HotTTL = 30 * time.Minute
	// HotHardCap 池容量事故保险丝（候选超限按分数截尾，正常永不触发）
	HotHardCap = 500000
	// HotGravity 热度分时间衰减陡度（越大越偏重新帖）
	HotGravity = 1.5
)

// 热度分信号默认权重（engagement = Σ wᵢ·ln(1+countᵢ)；整组零值时回退，单个 0 是合法权重）
const (
	HotWeightLike      = 1.0
	HotWeightFavourite = 1.5
	HotWeightComment   = 2.0
	HotWeightView      = 0.1
)
