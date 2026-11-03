package mq

import (
	"context"
	"fmt"
	"log/slog"
	"rednote/config"
	"rednote/gen/query"
	common "rednote/internal/common/models"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/trace"
	"strconv"
	"time"

	"github.com/bwmarrin/snowflake"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/gorm"
)

type JetStreamClient struct {
	conn *nats.Conn
	js   jetstream.JetStream
	log  *slog.Logger
}

// MQClient 消息队列客户端接口（四个方法全量抽象），
// 生产实现为 JetStreamClient，单元测试注入 test/testmq 的本地内存模拟。
type MQClient interface {
	// Send 非事务性消息，立即发送，不进发件箱
	Send(ctx context.Context, msg *MQMessage) error
	// SendTX 事务性消息，写入发件箱（out_box_entries），随业务事务提交
	SendTX(ctx context.Context, msg *MQMessage, tx *gorm.DB) error
	// GetStream 获取 JetStream 流控句柄（consumer 消费 / poller 异步发布使用）
	GetStream() jetstream.JetStream
	// Close 关闭连接
	Close()
}

var _ MQClient = (*JetStreamClient)(nil)

func NewJetStreamClient(cfg *config.Config, lf logger.LoggerFactory) (*JetStreamClient, func()) {

	c := &JetStreamClient{}
	conn, err := nats.Connect(cfg.JetStream.Url)
	if err != nil {
		panic(err)
	}

	client, err := jetstream.New(conn,
		// 为异步发布的 ack future 设置超时，否则 ack 丢失时 future 永不 resolve，）
		jetstream.WithPublishAsyncTimeout(defaults.AsyncPublishAckTimeout),
	)
	if err != nil {
		panic(err)
	}

	c.conn = conn
	c.js = client
	c.log = lf.GetLoggerOf("jetstream")

	return c, c.Close
}

// 非事务性消息 会立即发送 不进入发件箱
// 不会处理延迟
func (m *JetStreamClient) Send(ctx context.Context, msg *MQMessage) (err error) {

	headers := nats.Header{}

	id := msg.ID
	if id == 0 {
		return fmt.Errorf("invalid message ID")
	}
	headers.Set(defaults.MessageIDKey, strconv.FormatInt(id, 10))

	traceID := trace.FromContext(ctx)
	if traceID != "" {
		headers.Set(defaults.TraceIDKey, traceID)
	}

	headers.Set(defaults.SendAtKey, strconv.FormatInt(time.Now().UnixMilli(), 10))

	for k, v := range msg.Header {
		headers.Set(k, v)
	}

	_, err = m.js.PublishMsg(ctx, &nats.Msg{
		Subject: msg.Subject,
		Header:  headers,
		Data:    msg.PayLoad,
	},
		jetstream.WithMsgID(snowflake.ParseInt64(msg.ID).String()),
	)

	if err != nil {
		m.log.Error("failed to publish message",
			"msg_type", "direct",
			"id", msg.ID,
			"subject", msg.Subject,
			"send_at", msg.SendAt,
			"trace_id", traceID,
			"error", err,
		)
		return fmt.Errorf("failed to publish message: %w", err)
	}

	m.log.Info("MQ message sent",
		"msg_type", "direct",
		"id", msg.ID,
		"subject", msg.Subject,
		"send_at", msg.SendAt,
		"trace_id", traceID,
	)

	return nil
}

// 事务性消息 会进入发件箱 根据事务结果决定是否发送
// 会处理延迟
func (m *JetStreamClient) SendTX(ctx context.Context, msg *MQMessage, tx *gorm.DB) error {

	traceID, ok := msg.Header[defaults.TraceIDKey]
	if !ok {
		traceID = trace.FromContext(ctx)
	}

	entry := &common.OutBoxEntry{
		BaseModel: common.BaseModel{
			ID: msg.ID,
		},

		TraceID: traceID,

		SendAt:  msg.SendAt,
		Subject: msg.Subject,
		Header:  msg.Header,
		Payload: msg.PayLoad,

		Status: common.STATUS_PENDING,
	}

	err := query.Use(tx).OutBoxEntry.WithContext(ctx).Create(entry)
	if err != nil {
		m.log.Error("failed to create transactional message",
			"msg_type", "transactional",
			"id", msg.ID,
			"subject", msg.Subject,
			"send_at", msg.SendAt,
			"trace_id", traceID,
			"error", err,
		)
		return fmt.Errorf("failed to create transactional message: %w", err)
	}

	m.log.Info("MQ message sent",
		"msg_type", "transactional",
		"id", msg.ID,
		"subject", msg.Subject,
		"send_at", msg.SendAt,
		"trace_id", traceID,
	)
	return nil
}

func (m *JetStreamClient) GetStream() jetstream.JetStream {
	return m.js
}

func (m *JetStreamClient) Close() {
	m.conn.Close()
}
