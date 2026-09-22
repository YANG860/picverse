package logger

import (
	"context"
	"log/slog"
	"rednote/packages/defaults"
	"rednote/packages/trace"
)

type TraceHandler struct {
	h slog.Handler
}

func NewTraceHandler(h slog.Handler) *TraceHandler {
	return &TraceHandler{
		h: h,
	}
}

func (t *TraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.h.Enabled(ctx, level)
}

func (t *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	// 从 context 中提取 trace_id 并添加到日志记录
	if traceID := trace.FromContext(ctx); traceID != "" {
		r.AddAttrs(slog.String(defaults.TraceIDKey, traceID))
	}
	return t.h.Handle(ctx, r)
}

func (t *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{
		h: t.h.WithAttrs(attrs),
	}
}

func (t *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{
		h: t.h.WithGroup(name),
	}
}
