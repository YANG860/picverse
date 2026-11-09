package poller

import (
	"context"
	"log/slog"
	"time"

	"rednote/config"
	v1 "rednote/internal/discover/v1"
	"rednote/packages/defaults"
	"rednote/packages/logger"
)

// FanoutCleaner 收件箱保留期清理器：定时批量删除过保留期的收件箱行，维护 fanout_timelines 表大小。
// 口径：保留期按帖子时间（post_created_at，与游标一致），过保留期的帖子从收件箱视图消失；
// 不做删帖级联删除——已删帖行同样由本任务随保留期回收（见 doc/internal/discover/discover.md）。
type FanoutCleaner struct {
	repo v1.FanoutRepo

	retention time.Duration
	interval  time.Duration

	log *slog.Logger
}

// NewFanoutCleaner 创建收件箱保留期清理器：
// cfg.Fanout.Retention 为 0 使用默认 30 天，cfg.Fanout.CleanupInterval 为 0 使用默认 1h
func NewFanoutCleaner(repo v1.FanoutRepo, cfg *config.Config, lf logger.LoggerFactory) *FanoutCleaner {
	retention := cfg.Fanout.Retention
	if retention <= 0 {
		retention = defaults.FanoutRetention
	}
	interval := cfg.Fanout.CleanupInterval
	if interval <= 0 {
		interval = defaults.FanoutCleanupInterval
	}

	return &FanoutCleaner{
		repo:      repo,
		retention: retention,
		interval:  interval,
		log:       lf.GetLoggerOf("fanout_cleaner"),
	}
}

// Run 阻塞循环清理，ctx 取消即退出（过期行不会消失，中断的批次下轮继续）
func (c *FanoutCleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Info("fanout cleaner stopped")
			return
		case <-ticker.C:
			c.cleanOnce(ctx)
		}
	}
}

// cleanOnce 单轮清理：删除全部过保留期的行（repo 内部按批循环）。
// 失败只告警等下轮（不中断循环、不丢清理）
func (c *FanoutCleaner) cleanOnce(ctx context.Context) {
	cutoff := time.Now().Add(-c.retention).UnixMilli()
	deleted, err := c.repo.CleanupExpired(ctx, cutoff, defaults.FanoutCleanupBatch)
	if err != nil {
		c.log.Warn("fanout cleanup failed, retry next round", "cutoff_millis", cutoff, "error", err)
		return
	}
	if deleted > 0 {
		c.log.Info("fanout timelines cleaned", "deleted", deleted, "cutoff_millis", cutoff)
	}
}
