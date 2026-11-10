package consumer

import (
	"context"
	"log/slog"
	"os"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/mq"
	"rednote/packages/trace"
	"runtime/debug"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

// Consumer 消费者公共基座：消息中间件依赖 + 生命周期上下文 + 运行参数。
// 业务依赖（Repo/S3/Dedup/搜索引擎等）由各消费者自持，不入基座
type Consumer struct {
	MQ  mq.MQClient
	log *slog.Logger

	name       string
	maxRetry   int
	workingDir string

	ctx    context.Context
	cancel context.CancelFunc
}

const (
	ACK   = true
	RETRY = false
)

// newConsumer 构造消费者基座（生命周期上下文与公共依赖收敛，消除各消费者构造样板）。
// workingDir 为空则 Start 时跳过目录准备
func newConsumer(name string, maxRetry int, workingDir string,
	mqClient mq.MQClient, lf logger.LoggerFactory, loggerName string) Consumer {
	ctx, cancel := context.WithCancel(context.Background())
	return Consumer{
		MQ:  mqClient,
		log: lf.GetLoggerOf(loggerName),

		name:       name,
		maxRetry:   maxRetry,
		workingDir: workingDir,

		ctx:    ctx,
		cancel: cancel,
	}
}

// MessageLoop 泛型消息循环：P 为 oneof 外层 proto 消息（*postv1.PostMessage 等）。
// 模板方法：HandleMessage（header/trace/panic 兜底/unmarshal/结果日志/Ack-Nak）与
// Start/Close 生命周期全部收敛于此；子类只提供 dispatch（oneof type switch → 业务 handler），
// 业务 handler 的错误分类语义：ACK = 非法数据丢弃、RETRY = 瞬时错误重投（见 ParseMessageID 等工具）。
type MessageLoop[P proto.Message] struct {
	Consumer
	stream     string
	newMsg     func() P
	dispatch   func(ctx context.Context, m P, messageID string) (ack bool, err error)
	ConsumeCtx jetstream.ConsumeContext
}

// NewMessageLoop 创建消息循环。stream 为 JetStream 流名；newMsg 构造空消息供 Unmarshal；
// dispatch 为业务分发（oneof type switch，未知类型返回 ACK + error）；messageID 供幂等 handler（DedupHandle）使用
func NewMessageLoop[P proto.Message](base Consumer, stream string,
	newMsg func() P, dispatch func(ctx context.Context, m P, messageID string) (ack bool, err error)) *MessageLoop[P] {
	return &MessageLoop[P]{Consumer: base, stream: stream, newMsg: newMsg, dispatch: dispatch}
}

// HandleMessage 消息循环模板：header 提取 → panic 兜底 → Unmarshal（失败 Term）→ dispatch → 结果日志 → Ack/Nak
func (l *MessageLoop[P]) HandleMessage(msg jetstream.Msg) {
	traceID := msg.Headers().Get(defaults.TraceIDKey)
	sendAt := msg.Headers().Get(defaults.SendAtKey)
	id := msg.Headers().Get(defaults.MessageIDKey)
	ctx := trace.WithTraceID(l.ctx, traceID)

	defer func() {
		if r := recover(); r != nil {
			l.log.Error("panic occurred",
				"trace_id", traceID,
				"send_at", sendAt,
				"id", id,
				"error", r,
				"stack", debug.Stack(),
			)
		}
	}()

	m := l.newMsg()
	if err := proto.Unmarshal(msg.Data(), m); err != nil {
		_ = msg.Term()
		l.log.Error("failed to unmarshal message",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
			"error", err,
		)
		return
	}

	ack, err := l.dispatch(ctx, m, id)
	msgType := oneofCaseName(m)
	if err != nil {
		l.log.Error("failed to handle message",
			"id", id,
			"type", msgType,
			"trace_id", traceID,
			"send_at", sendAt,
			"error", err,
		)
	} else {
		l.log.Info("successfully handled message",
			"id", id,
			"type", msgType,
			"trace_id", traceID,
			"send_at", sendAt,
		)
	}

	// Ack/Nak 失败不吞错：Ack 失败会导致消息重投（AtLeastOnce），需可追溯
	if ack {
		if ackErr := msg.Ack(); ackErr != nil {
			l.log.Warn("message ack failed，将重投", "id", id, "error", ackErr)
		}
	} else if nakErr := msg.Nak(); nakErr != nil {
		l.log.Warn("message nak failed", "id", id, "error", nakErr)
	}
}

// Start 启动消费：可选工作目录 → 创建/更新幂等消费者（同构配置）→ 开始消费
func (l *MessageLoop[P]) Start() {
	l.log.Info("starting consumer...", "name", l.name, "stream", l.stream)
	if l.workingDir != "" {
		if err := os.MkdirAll(l.workingDir, 0755); err != nil {
			l.log.Error("failed to create working directory", "error", err)
			panic(err)
		}
	}

	consumer, err := l.MQ.GetStream().CreateOrUpdateConsumer(l.ctx, l.stream,
		jetstream.ConsumerConfig{
			Name:    l.name,
			Durable: l.name,

			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckPolicy:     jetstream.AckExplicitPolicy,

			MaxDeliver: l.maxRetry,
		})
	if err != nil {
		l.log.Error("failed to create or update consumer", "error", err)
		panic(err)
	}

	consumeCtx, err := consumer.Consume(l.HandleMessage)
	if err != nil {
		panic(err)
	}
	l.ConsumeCtx = consumeCtx
	l.log.Info("consumer started", "name", l.name)
}

// Close 停止消费并释放生命周期
func (l *MessageLoop[P]) Close() {
	l.cancel()
	l.ConsumeCtx.Stop()
}

// oneofCaseName 取 oneof case 的短名（proto 字段名，如 "audit_post"）作日志类型标识
func oneofCaseName(m proto.Message) string {
	rm := m.ProtoReflect()
	oneofs := rm.Descriptor().Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		if f := rm.WhichOneof(oneofs.Get(i)); f != nil {
			return string(f.Name())
		}
	}
	return "unknown"
}
