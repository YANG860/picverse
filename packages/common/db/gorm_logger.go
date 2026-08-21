package db

import (
	"context"
	"log/slog"
	"time"

	gormlogger "gorm.io/gorm/logger"
)

// slogAdapter 将 slog.Logger 适配为 gormlogger.Interface
type slogAdapter struct {
	logger *slog.Logger
}

func NewSlogAdapter(l *slog.Logger) gormlogger.Interface {
	return &slogAdapter{logger: l}
}

func (a *slogAdapter) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return a
}

func (a *slogAdapter) Info(ctx context.Context, msg string, data ...any) {
	a.logger.InfoContext(ctx, msg, data...)
}

func (a *slogAdapter) Warn(ctx context.Context, msg string, data ...any) {
	a.logger.WarnContext(ctx, msg, data...)
}

func (a *slogAdapter) Error(ctx context.Context, msg string, data ...any) {
	a.logger.ErrorContext(ctx, msg, data...)
}

func (a *slogAdapter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	sql, rows := fc()
	a.logger.DebugContext(ctx, "gorm trace",
		"sql", sql,
		"rows", rows,
		"elapsed", elapsed.String(),
		"err", err,
	)
}
