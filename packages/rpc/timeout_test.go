package rpc

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// TestTimeoutInterceptor_InjectsDeadline ctx 无 deadline：注入 now+timeout
func TestTimeoutInterceptor_InjectsDeadline(t *testing.T) {
	interceptor := NewTimeoutInterceptor(50 * time.Millisecond)
	var gotDeadline time.Time
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		dl, ok := ctx.Deadline()
		require.True(t, ok, "无 deadline 的 ctx 应被注入")
		gotDeadline = dl
		return nil, nil
	}
	fn := interceptor.WrapUnary(next)

	start := time.Now()
	_, err := fn(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
	assert.WithinDuration(t, start.Add(50*time.Millisecond), gotDeadline, 20*time.Millisecond,
		"deadline 应为 now+timeout")
}

// TestTimeoutInterceptor_KeepsTighterDeadline 调用方已有更紧 deadline：只收紧不放宽
func TestTimeoutInterceptor_KeepsTighterDeadline(t *testing.T) {
	interceptor := NewTimeoutInterceptor(5 * time.Second)
	want := time.Now().Add(50 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()

	var gotDeadline time.Time
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		gotDeadline, _ = ctx.Deadline()
		return nil, nil
	}
	fn := interceptor.WrapUnary(next)

	_, err := fn(ctx, connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
	assert.WithinDuration(t, want, gotDeadline, time.Millisecond,
		"调用方更紧的 deadline 不应被放宽")
}

// TestTimeoutInterceptor_ZeroTimeout timeout<=0 不注入（防御：调用方应先经 defaults 兜底）
func TestTimeoutInterceptor_ZeroTimeout(t *testing.T) {
	interceptor := NewTimeoutInterceptor(0)
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		_, ok := ctx.Deadline()
		assert.False(t, ok, "timeout<=0 不应注入 deadline")
		return nil, nil
	}
	fn := interceptor.WrapUnary(next)

	_, err := fn(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
}

// TestTimeoutInterceptor_Expires 下游挂死：ctx 按时取消（不无限挂），错误可识别为 DeadlineExceeded
func TestTimeoutInterceptor_Expires(t *testing.T) {
	interceptor := NewTimeoutInterceptor(20 * time.Millisecond)
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		<-ctx.Done() // 模拟下游挂死
		return nil, ctx.Err()
	}
	fn := interceptor.WrapUnary(next)

	start := time.Now()
	_, err := fn(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second, "超时应按时触发，不无限挂")
}
