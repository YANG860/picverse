package rpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
)

// timeoutInterceptor 客户端调用超时拦截器：unary 调用统一注入 deadline。
// per-attempt 计时（为 P2 失败转移预留：每次尝试独立计时、共享调用方总 deadline）。
//
// 只收紧不放宽：调用方 ctx 已有更紧 deadline 时不叠加，尊重上游预算
type timeoutInterceptor struct {
	timeout time.Duration
}

// NewTimeoutInterceptor 构造客户端超时拦截器。
// timeout <= 0 不注入（调用方应先经 defaults.RPCDefaultTimeout 兜底）。
// 流式调用不注入（项目 RPC 均为 unary；流式超时语义需按整条流生命周期另行设计）
func NewTimeoutInterceptor(timeout time.Duration) connect.Interceptor {
	return &timeoutInterceptor{timeout: timeout}
}

func (t *timeoutInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if t.timeout <= 0 {
			return next(ctx, req)
		}
		// 只收紧不放宽：调用方剩余预算更小（或相等）时维持原 deadline
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= t.timeout {
			return next(ctx, req)
		}
		ctx, cancel := context.WithTimeout(ctx, t.timeout)
		defer cancel()
		return next(ctx, req)
	}
}

func (t *timeoutInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (t *timeoutInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
