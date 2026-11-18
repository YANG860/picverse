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
	"time"

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
	// handleTimeout 单条消息处理超时（防单条挂死阻塞消息循环），<=0 不限制
	handleTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
}

const (
	ACK   = true
	RETRY = false
)

// newConsumer 构造消费者基座（生命周期上下文与公共依赖收敛，消除各消费者构造样板）。
// workingDir 为空则 Start 时跳过目录准备；handleTimeout <=0 用默认 30s（单条消息处理超时）
func newConsumer(name string, maxRetry int, workingDir string, handleTimeout time.Duration,
	mqClient mq.MQClient, lf logger.LoggerFactory, loggerName string) Consumer {
	ctx, cancel := context.WithCancel(context.Background())
	if handleTimeout <= 0 {
		handleTimeout = defaults.RPCConsumerTimeout
	}
	return Consumer{
		MQ:  mqClient,
		log: lf.GetLoggerOf(loggerName),

		name:          name,
		maxRetry:      maxRetry,
		workingDir:    workingDir,
		handleTimeout: handleTimeout,

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

// deliveredKey 投递序号在 ctx 中的键（At-least-once 语义下消息第几次投递，1=首次）
type deliveredKey struct{}

// withDelivered 把 JetStream 投递序号注入 ctx，供 handler 判“重试耗尽判失败”
// （元数据缺失时不注入，DeliveredFrom 视同首次）
func withDelivered(ctx context.Context, md *jetstream.MsgMetadata) context.Context {
	if md == nil {
		return ctx
	}
	return context.WithValue(ctx, deliveredKey{}, md.NumDelivered)
}

// DeliveredFrom 取本条消息的投递序号（未注入时视同首次投递——保守不判耗尽）
func DeliveredFrom(ctx context.Context) uint64 {
	if v, ok := ctx.Value(deliveredKey{}).(uint64); ok {
		return v
	}
	return 1
}

// HandleMessage 消息循环模板：header 提取 → panic 兜底 → Unmarshal（失败 Term）→ dispatch → 结果日志 → Ack/Nak
func (l *MessageLoop[P]) HandleMessage(msg jetstream.Msg) {
	traceID := msg.Headers().Get(defaults.TraceIDKey)
	sendAt := msg.Headers().Get(defaults.SendAtKey)
	id := msg.Headers().Get(defaults.MessageIDKey)
	ctx := trace.WithTraceID(l.ctx, traceID)
	// 投递序号注入：供 handler 区分“继续重试”与“窗口耗尽判失败”
	if md, mdErr := msg.Metadata(); mdErr == nil {
		ctx = withDelivered(ctx, md)
	}
	l.log.Info("message received", "name", l.name, "id", id) // 分水岭：回调进门即打（定位库层丢 or 业务层丢）

	// settled 标记本条消息已 Ack/Nak（panic 兜底据此决定是否补 Nak，避免对已确认消息二次 Nak）
	settled := false
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("panic occurred",
				"trace_id", traceID,
				"send_at", sendAt,
				"id", id,
				"error", r,
				"stack", debug.Stack(),
			)
			// panic 也是失败：显式 Nak 走统一退避序列。不补 Nak 的话消息只能靠 AckWait（30s）到期重投，
			// 节奏与退避序列不一致、也进不了"failed to handle message"的重投日志。
			// 注意：panic 不判耗尽兜底（业务侧终态兜底只覆盖常规错误路径），耗尽后仍需人工介入
			if !settled {
				if nakErr := msg.NakWithDelay(retryDelay(ctx)); nakErr != nil {
					l.log.Warn("message nak failed after panic", "id", id, "error", nakErr)
				}
				settled = true
			}
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

	// 单条消息处理超时（防挂死阻塞消息循环）：超时 → dispatch 内 DeadlineExceeded → RETRY 重投；
	// handleTimeout <=0 不限制（直接构造的测试基座不注入）
	dctx := ctx
	if l.handleTimeout > 0 {
		var cancel context.CancelFunc
		dctx, cancel = context.WithTimeout(ctx, l.handleTimeout)
		defer cancel()
	}
	ack, err := l.dispatch(dctx, m, id)
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
	} else if nakErr := msg.NakWithDelay(retryDelay(ctx)); nakErr != nil {
		l.log.Warn("message nak failed", "id", id, "error", nakErr)
	}
	settled = true
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

			// Nak 后退避重投（全组统一节奏）：防止瞬时错误重投风暴挤占串行消费循环；
			// 超出长度沿用最后一个值，总投递次数仍由 MaxDeliver 控制
			// 刻意不用 consumer 级 BackOff：NATS 的 BackOff 会**覆盖 AckWait**，并把 ack_wait 隐式设成
			// BackOff[0]（=1s）——任何耗时超 1s 的 handler 都会在处理中被判超时重投（在途重投），
			// 而消费循环是串行的，重投与在途处理交错就造成重复转换/重复扇出（实测同一媒体资产处理
			// 2~3 次、fanout 累计 263 次重投）。退避改由 NakWithDelay 逐条下发（见 HandleMessage）。
			// AckWait 只兜"进程挂死"：对齐单条处理超时，正常 handler 由 ctx 超时自我了断
			AckWait: l.handleTimeout,
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

// retryDelay 取本次失败后的退避时长：按投递序号索引 ConsumerRetryBackOff（首次失败等 1s，
// 超出长度沿用最后一个值）。与 MaxDeliver 一起构成"重投窗口"
func retryDelay(ctx context.Context) time.Duration {
	bo := defaults.ConsumerRetryBackOff
	idx := min(max(int(DeliveredFrom(ctx))-1, 0), len(bo)-1)
	return bo[idx]
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
