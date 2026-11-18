package rpc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// newBulkheadCall 把 next 包为可直接调用的 unary 链
func newBulkheadCall(interceptor connect.Interceptor,
	next func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error),
) func(ctx context.Context) (connect.AnyResponse, error) {
	fn := interceptor.WrapUnary(next)
	return func(ctx context.Context) (connect.AnyResponse, error) {
		return fn(ctx, connect.NewRequest(&emptypb.Empty{}))
	}
}

// TestBulkheadInterceptor_RejectsWhenFull 满舱：快速失败 CodeResourceExhausted，next 不被调用
func TestBulkheadInterceptor_RejectsWhenFull(t *testing.T) {
	interceptor := NewBulkheadInterceptor(1)

	// 第一调用占住额度（阻塞在 next 内直到放行）
	entered := make(chan struct{})
	release := make(chan struct{})
	first := newBulkheadCall(interceptor, func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		close(entered)
		<-release
		return connect.NewResponse(&emptypb.Empty{}), nil
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = first(context.Background())
	}()
	<-entered // 额度已被占用

	// 满舱拒绝：快速失败且 next 不被调用
	var secondCalled atomic.Bool
	second := newBulkheadCall(interceptor, func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		secondCalled.Store(true)
		return connect.NewResponse(&emptypb.Empty{}), nil
	})
	start := time.Now()
	_, err := second(context.Background())
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	assert.False(t, secondCalled.Load(), "满舱时 next 不应被调用（请求不发出）")
	assert.Less(t, time.Since(start), 100*time.Millisecond, "满舱应快速失败不排队")

	close(release)
	wg.Wait()
}

// TestBulkheadInterceptor_ReleasesAfterReturn 调用返回即归还额度：满舱拒绝后额度可复用
func TestBulkheadInterceptor_ReleasesAfterReturn(t *testing.T) {
	interceptor := NewBulkheadInterceptor(1)
	call := newBulkheadCall(interceptor, func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	})

	// 顺序两轮：第一轮占满并归还，第二轮才能通过
	_, err := call(context.Background())
	require.NoError(t, err)
	_, err = call(context.Background())
	require.NoError(t, err, "额度应已归还可复用")
}

// TestBulkheadInterceptor_ReleasesOnPanic panic 路径也归还额度（defer 语义）
func TestBulkheadInterceptor_ReleasesOnPanic(t *testing.T) {
	interceptor := NewBulkheadInterceptor(1)
	panicking := newBulkheadCall(interceptor, func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		panic("boom")
	})
	assert.Panics(t, func() { _, _ = panicking(context.Background()) })

	// panic 展开时 defer Release 已执行：后续调用可复用额度
	ok := newBulkheadCall(interceptor, func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	})
	_, err := ok(context.Background())
	require.NoError(t, err, "panic 后额度应已归还")
}

// TestBulkheadInterceptor_ZeroUnlimited maxInflight<=0 不限制（调用方应先经 defaults 兜底）
func TestBulkheadInterceptor_ZeroUnlimited(t *testing.T) {
	interceptor := NewBulkheadInterceptor(0)
	var called atomic.Int32
	call := newBulkheadCall(interceptor, func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		called.Add(1)
		return connect.NewResponse(&emptypb.Empty{}), nil
	})

	for i := 0; i < 3; i++ {
		_, err := call(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, int32(3), called.Load())
}

// TestBulkheadInterceptor_ConcurrencyCeiling 并发正确性（-race）：在途并发恒不超过上限
func TestBulkheadInterceptor_ConcurrencyCeiling(t *testing.T) {
	const limit = 3
	interceptor := NewBulkheadInterceptor(limit)

	var cur, maxObserved, rejected atomic.Int32
	call := newBulkheadCall(interceptor, func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		now := cur.Add(1)
		for {
			old := maxObserved.Load()
			if now <= old || maxObserved.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond) // 模拟在途耗时
		cur.Add(-1)
		return connect.NewResponse(&emptypb.Empty{}), nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := call(context.Background())
			if err != nil {
				rejected.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.LessOrEqual(t, maxObserved.Load(), int32(limit), "在途并发不得超过上限")
	assert.Greater(t, maxObserved.Load(), int32(0), "应有调用通过")
	assert.Greater(t, rejected.Load(), int32(0), "超额并发应被拒绝")
}
