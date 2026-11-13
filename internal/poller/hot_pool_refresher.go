package poller

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"rednote/config"
	discovermodels "rednote/internal/discover/models"
	v1 "rednote/internal/discover/v1"
	"rednote/packages/cache"
	"rednote/packages/defaults"
	"rednote/packages/logger"
)

// HotPoolRefresher 热榜池维护器：定时整池重建（入池/算分/淘汰/TTL 续期一步完成）。
// 候选 = posts 表 PUBLISHED + 时间窗（统计列即互动汇总事实，无需互动事件链路）；
// 逐帖算热度分 → build key 写满 → RENAME 原子换热池（见 doc/internal/discover/plan_recommend_hot.md）。
// 时间/状态淘汰 = 下轮候选不再覆盖（零显式删除）；失败只告警等下轮，旧池继续服务。
type HotPoolRefresher struct {
	repo v1.HotRepo
	pool *cache.HotPoolCache

	interval time.Duration
	window   time.Duration
	hardCap  int
	gravity  float64
	weights  config.HotWeights

	log *slog.Logger
}

// NewHotPoolRefresher 创建热池维护器（cfg.Hot 为 0 的项回退 defaults/hot.go 默认值）。
// Weights 整组零值使用默认组，单个 0 是合法权重
func NewHotPoolRefresher(repo v1.HotRepo, pool *cache.HotPoolCache, cfg *config.Config, lf logger.LoggerFactory) *HotPoolRefresher {
	h := cfg.Hot
	interval := h.RefreshInterval
	if interval <= 0 {
		interval = defaults.HotRefreshInterval
	}
	window := h.Window
	if window <= 0 {
		window = defaults.HotWindow
	}
	hardCap := h.HardCap
	if hardCap <= 0 {
		hardCap = defaults.HotHardCap
	}
	gravity := h.Gravity
	if gravity <= 0 {
		gravity = defaults.HotGravity
	}
	weights := h.Weights
	if weights.Zero() {
		weights = config.HotWeights{
			Like:      defaults.HotWeightLike,
			Favourite: defaults.HotWeightFavourite,
			Comment:   defaults.HotWeightComment,
			View:      defaults.HotWeightView,
		}
	}

	return &HotPoolRefresher{
		repo:     repo,
		pool:     pool,
		interval: interval,
		window:   window,
		hardCap:  hardCap,
		gravity:  gravity,
		weights:  weights,
		log:      lf.GetLoggerOf("hot_pool_refresher"),
	}
}

// Run 阻塞循环维护，ctx 取消即退出
func (r *HotPoolRefresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("hot pool refresher stopped")
			return
		case <-ticker.C:
			r.refreshOnce(ctx)
		}
	}
}

// refreshOnce 单轮重建：窗口候选 → 算分（含保险丝截尾）→ RENAME 原子换。
// 失败只告警等下轮（旧池继续服务，TTL 停摆兜底）
func (r *HotPoolRefresher) refreshOnce(ctx context.Context) {
	start := time.Now()
	cutoff := start.Add(-r.window).UnixMilli()
	cands, err := r.repo.ListHotCandidates(ctx, cutoff)
	if err != nil {
		r.log.Warn("hot candidates load failed, retry next round", "cutoff_millis", cutoff, "error", err)
		return
	}

	entries := r.score(cands, start)

	// 保险丝：超 hardCap 按分数截尾（降序尾部先出 = 0 分/低分先出），防事故性巨池
	if r.hardCap > 0 && len(entries) > r.hardCap {
		entries = entries[:r.hardCap]
	}

	if err := r.pool.Rebuild(ctx, entries); err != nil {
		r.log.Warn("hot pool rebuild failed, retry next round", "error", err)
		return
	}
	r.log.Info("hot pool rebuilt", "candidates", len(cands), "entries", len(entries), "elapsed_ms", time.Since(start).Milliseconds())
}

// score 全量算分 + 保险丝截尾：engagement = Σ wᵢ·ln(1+countᵢ)，
// score = engagement / (age_hours + 2)^gravity。
// 候选超 hardCap 按分数降序截尾（0 分新帖先出——事故保险丝，正常永不触发）
func (r *HotPoolRefresher) score(cands []discovermodels.HotCandidate, now time.Time) []cache.HotEntry {
	entries := make([]cache.HotEntry, 0, len(cands))
	for _, c := range cands {
		ageHours := now.Sub(time.UnixMilli(c.CreatedAt)).Hours()
		if ageHours < 0 {
			ageHours = 0 // 时钟回拨/未来时间戳防御
		}
		engagement := r.weights.Like*math.Log1p(float64(c.LikeCount)) +
			r.weights.Favourite*math.Log1p(float64(c.FavouriteCount)) +
			r.weights.Comment*math.Log1p(float64(c.CommentCount)) +
			r.weights.View*math.Log1p(float64(c.ViewCount))
		sc := engagement / math.Pow(ageHours+2, r.gravity)
		entries = append(entries, cache.HotEntry{PostID: c.PostID, AuthorID: c.AuthorID, Score: sc})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Score > entries[j].Score })

	return entries
}
