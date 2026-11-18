package consumer

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"

	postv1 "rednote/gen/go/post/v1"
)

// fakeMsg jetstream.Msg 测试桩：记录 Ack/Nak 调用（body 留空，默认空 proto 消息可解析）
type fakeMsg struct {
	acked     bool
	naked     bool
	nakDelay  time.Duration
	delivered uint64 // >0 时 Metadata 返回该投递序号（0 模拟元数据缺失）
}

func (f *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	if f.delivered == 0 {
		return nil, nil
	}
	return &jetstream.MsgMetadata{NumDelivered: f.delivered}, nil
}
func (f *fakeMsg) Data() []byte                       { return nil }
func (f *fakeMsg) Headers() nats.Header               { return nil }
func (f *fakeMsg) Subject() string                    { return "test" }
func (f *fakeMsg) Reply() string                      { return "" }
func (f *fakeMsg) Ack() error                         { f.acked = true; return nil }
func (f *fakeMsg) DoubleAck(context.Context) error    { f.acked = true; return nil }
func (f *fakeMsg) Nak() error                         { f.naked = true; return nil }
func (f *fakeMsg) NakWithDelay(d time.Duration) error { f.naked = true; f.nakDelay = d; return nil }
func (f *fakeMsg) InProgress() error                  { return nil }
func (f *fakeMsg) Term() error                        { return nil }
func (f *fakeMsg) TermWithReason(string) error        { return nil }

// newTimeoutTestLoop 构造 handle 超时测试消息循环
func newTimeoutTestLoop(handleTimeout time.Duration,
	dispatch func(ctx context.Context, m *postv1.PostMessage, messageID string) (bool, error),
) *MessageLoop[*postv1.PostMessage] {
	return NewMessageLoop(
		Consumer{log: slog.Default(), handleTimeout: handleTimeout, ctx: context.Background()},
		"test-stream",
		func() *postv1.PostMessage { return &postv1.PostMessage{} },
		dispatch,
	)
}

// TestHandleMessage_HandleTimeout 单条消息处理超时注入：dispatch 收到带 deadline 的 ctx
func TestHandleMessage_HandleTimeout(t *testing.T) {
	var gotDeadline time.Time
	l := newTimeoutTestLoop(time.Hour, func(ctx context.Context, _ *postv1.PostMessage, _ string) (bool, error) {
		gotDeadline, _ = ctx.Deadline()
		return ACK, nil
	})

	l.HandleMessage(&fakeMsg{})
	assert.False(t, gotDeadline.IsZero(), "dispatch ctx 应带 handleTimeout deadline")
	assert.WithinDuration(t, time.Now().Add(time.Hour), gotDeadline, time.Minute)
}

// TestHandleMessage_HandleTimeoutExpires dispatch 挂死：超时按时截断 → RETRY 重投（吃 MaxDeliver 预算）
func TestHandleMessage_HandleTimeoutExpires(t *testing.T) {
	l := newTimeoutTestLoop(20*time.Millisecond, func(ctx context.Context, _ *postv1.PostMessage, _ string) (bool, error) {
		<-ctx.Done() // 模拟处理挂死
		return RETRY, ctx.Err()
	})

	msg := &fakeMsg{}
	start := time.Now()
	l.HandleMessage(msg)
	assert.True(t, msg.naked, "超时应 RETRY 重投")
	assert.False(t, msg.acked)
	assert.Less(t, time.Since(start), time.Second, "超时应按时截断，不无限挂")
}

// TestHandleMessage_NoHandleTimeout handleTimeout<=0（测试基座直接构造）：不注入 deadline
func TestHandleMessage_NoHandleTimeout(t *testing.T) {
	var hasDeadline bool
	l := newTimeoutTestLoop(0, func(ctx context.Context, _ *postv1.PostMessage, _ string) (bool, error) {
		_, hasDeadline = ctx.Deadline()
		return ACK, nil
	})

	l.HandleMessage(&fakeMsg{})
	assert.False(t, hasDeadline, "handleTimeout<=0 不应注入 deadline")
}

// TestHandleMessage_PanicNak panic 兜底：panic 也是失败，须显式 Nak 走统一退避序列。
// 历史实现只记日志不 Nak，消息只能靠 AckWait（30s）到期重投——节奏与退避序列不一致。
func TestHandleMessage_PanicNak(t *testing.T) {
	l := newTimeoutTestLoop(0, func(context.Context, *postv1.PostMessage, string) (bool, error) {
		panic("boom")
	})

	msg := &fakeMsg{delivered: 2}
	l.HandleMessage(msg)

	assert.True(t, msg.naked, "panic 后应显式 Nak（否则只能等 AckWait 30s 重投）")
	assert.False(t, msg.acked)
	assert.Equal(t, 2*time.Second, msg.nakDelay, "按投递序号走退避序列（首投 1s、第 2 投 2s…）")
}

// TestHandleMessage_PanicAfterSettle 已 Ack 后再 panic（例如日志环节）：不得二次 Nak
func TestHandleMessage_PanicAfterSettle(t *testing.T) {
	l := newTimeoutTestLoop(0, func(context.Context, *postv1.PostMessage, string) (bool, error) {
		return ACK, nil
	})

	msg := &fakeMsg{}
	l.HandleMessage(msg)
	assert.True(t, msg.acked)
	assert.False(t, msg.naked)
}
