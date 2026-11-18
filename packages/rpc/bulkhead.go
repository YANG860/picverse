package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"golang.org/x/sync/semaphore"
)

// bulkheadInterceptor 舱壁拦截器（调用端限流）：per-service 在途请求并发上限。
// 防单一慢/故障下游的在途请求无限堆积（雪崩的资源侧根因）——
// 堆积资源上界 = maxInflight × 调用超时（超时保证额度不泄漏）。
//
// 快速失败不排队：排队会把过载变慢失败（与超时堆积同病），满舱立即拒绝、请求不发出
type bulkheadInterceptor struct {
	sem *semaphore.Weighted // nil = 不限制（maxInflight <= 0）
}

// NewBulkheadInterceptor 构造舱壁拦截器（unary；流式不拦截，项目 RPC 均为 unary）。
// 每客户端实例独立持有信号量 = per-service 一个配额（隔离舱语义：故障服务耗尽自己的额度，
// 不拖累其他下游）；一次 Acquire 覆盖整条尝试链（transport 转移在内层，不重复计数）。
// maxInflight <= 0 不限制（调用方应先经 defaults.RPCMaxInflight 兜底）
func NewBulkheadInterceptor(maxInflight int64) connect.Interceptor {
	b := &bulkheadInterceptor{}
	if maxInflight > 0 {
		b.sem = semaphore.NewWeighted(maxInflight)
	}
	return b
}

func (b *bulkheadInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if b.sem == nil {
			return next(ctx, req)
		}
		// TryAcquire 非阻塞：满舱快速失败（CodeResourceExhausted，调用方按连接层码分类处理）
		if !b.sem.TryAcquire(1) {
			return nil, connect.NewError(connect.CodeResourceExhausted,
				errors.New("in-flight limit reached for "+req.Spec().Procedure))
		}
		// 全路径释放（成功/失败/超时/panic 均经 defer 归还额度）
		defer b.sem.Release(1)
		return next(ctx, req)
	}
}

func (b *bulkheadInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (b *bulkheadInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
