package poller

import (
	"context"
	"log/slog"
	"time"

	"rednote/config"
	"rednote/internal/post/v1"
	"rednote/packages/cache"
	"rednote/packages/logger"
)

// RedisPoller Redis 浏览计数刷回器（PV 近似口径）：
// 定时把 Redis 累积的浏览增量原子取走（GETDEL）后批量写回 posts.view_count。
//
// 口径：只丢不重——取走在前、写库在后，任一环节失败丢该批增量（PV 近似可接受），
// 不会产生重复计数；多实例并发安全（取走是原子操作）。
// 写库失败不回填 Redis：IncPostViewCount 单语句执行的成败在异常场景下可能不明确，
// 回填有双计风险，宁丢不重。
type RedisPoller struct {
	Repo  v1.PostRepository
	Cache cache.PostEntityCache

	interval time.Duration
	log      *slog.Logger
}

// NewRedisPoller 创建浏览计数刷回器
// cfg.Post.ViewFlushInterval 为 0 时使用默认 30s
func NewRedisPoller(repo v1.PostRepository, pcache cache.PostEntityCache, cfg *config.Config, lf logger.LoggerFactory) *RedisPoller {
	interval := cfg.Post.ViewFlushInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	return &RedisPoller{
		Repo:     repo,
		Cache:    pcache,
		interval: interval,
		log:      lf.GetLoggerOf("redis_poller"),
	}
}

// Run 阻塞循环刷回，ctx 取消即尽力冲刷残留后退出。
// 刷回统一使用脱离循环的短超时上下文：关停瞬间在途批次照常完成，不因取消而丢弃。
func (p *RedisPoller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// 退出前尽力冲刷残留增量（best-effort）
			p.flushDetached()
			p.log.Info("redis poller stopped")
			return
		case <-ticker.C:
			p.flushDetached()
		}
	}
}

// flushDetached 以独立短超时上下文执行单轮刷回（不受循环 ctx 取消影响）
func (p *RedisPoller) flushDetached() {
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.flushOnce(flushCtx)
}

// flushOnce 单轮刷回：取走全部增量 → 逐帖写回（批量小、逐条失败互不影响）
func (p *RedisPoller) flushOnce(ctx context.Context) {
	deltas, err := p.Cache.TakePostViewDeltas(ctx)
	if err != nil {
		// 取走失败：增量仍留在 Redis，下轮再试（不丢失）
		p.log.Warn("take view count deltas failed, skip this round", "error", err)
		return
	}
	if len(deltas) == 0 {
		return
	}

	flushed := 0
	for postID, delta := range deltas {
		info, err := p.Repo.IncPostViewCount(ctx, postID, delta)
		if err != nil {
			// 已取走的增量写库失败即丢弃（只丢不重口径）
			p.log.Warn("write back view count failed, delta dropped", "post_id", postID, "delta", delta, "error", err)
			continue
		}
		if info.RowsAffected == 0 {
			p.log.Info("view count dropped: post not found", "post_id", postID, "delta", delta)
			continue
		}
		flushed++
	}
	p.log.Info("view counts flushed", "posts", flushed, "dropped", len(deltas)-flushed)
}
