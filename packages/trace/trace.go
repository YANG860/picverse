package trace

import (
	"context"
)

type traceID struct{}

func WithTraceID(ctx context.Context, id string) context.Context {
	if id != "" {
		return context.WithValue(ctx, traceID{}, id)
	}
	return ctx
}

func FromContext(ctx context.Context) string {
	if traceID, ok := ctx.Value(traceID{}).(string); ok {
		return traceID
	}
	return ""
}
