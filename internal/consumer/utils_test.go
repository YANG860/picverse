package consumer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
)

// TestClassifyErr 错误分类回归（契约见 doc/internal/consumer/consumer.md）：
// 瞬时 → RETRY 重投，永久 → ACK 丢弃；哨兵与 connect 错误均须穿透 wrap 链识别
func TestClassifyErr(t *testing.T) {
	// 瞬时（RETRY）：连接层瞬时码
	for _, code := range []connect.Code{
		connect.CodeUnavailable, connect.CodeUnknown,
		connect.CodeDeadlineExceeded, connect.CodeResourceExhausted,
	} {
		assert.Equal(t, RETRY, classifyErr(connect.NewError(code, errors.New("x"))), "code=%v 应 RETRY", code)
	}
	// 瞬时（RETRY）：非 connect 依赖错误（DB/Redis/Meili/S3 抖动、网络错误）
	assert.Equal(t, RETRY, classifyErr(errors.New("db down")), "非 connect 依赖错误按瞬时处理")
	// 瞬时（RETRY）：wrap 链上的 connect 错误同样识别
	assert.Equal(t, RETRY, classifyErr(fmt.Errorf("outer: %w",
		connect.NewError(connect.CodeUnavailable, errors.New("x")))), "wrap 链上的瞬时 connect 错误")

	// 永久（ACK）：连接层永久码（重投只会得到同样结果）
	for _, code := range []connect.Code{
		connect.CodeInvalidArgument, connect.CodeUnimplemented,
		connect.CodeInternal, connect.CodePermissionDenied,
	} {
		assert.Equal(t, ACK, classifyErr(connect.NewError(code, errors.New("x"))), "code=%v 应 ACK", code)
	}
	// 永久（ACK）：业务 fail 终态哨兵，且穿透多层 wrap
	assert.Equal(t, ACK, classifyErr(fmt.Errorf("user.GetFollowers fail: %w", errBizFail)), "业务 fail 终态")
	assert.Equal(t, ACK, classifyErr(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", errBizFail))), "哨兵穿透 wrap 链")
}

// TestRetryDelay NakWithDelay 逐条退避：按投递序号取 ConsumerRetryBackOff，超出沿用末值。
// 不用 consumer 级 BackOff 的原因见 HandleMessage 注释（BackOff 会覆盖 AckWait → 在途重投）
func TestRetryDelay(t *testing.T) {
	cases := []struct {
		delivered uint64
		want      time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 3 * time.Second},
		{4, 5 * time.Second},
		{5, 5 * time.Second},
		{99, 5 * time.Second},
	}
	for _, c := range cases {
		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: c.delivered})
		assert.Equal(t, c.want, retryDelay(ctx), "第 %d 次投递失败后的退避", c.delivered)
	}
	// 元数据缺失（直接构造的测试基座/非 JetStream 消息）：按首次处理
	assert.Equal(t, time.Second, retryDelay(context.Background()))
}
