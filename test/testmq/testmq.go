// Package testmq 提供 mq.MQClient 的本地内存模拟，用于单元测试注入：
//   - Send/SendTX 记录消息供断言（SendTX 不写 outbox 表；outbox 事务语义由真实现 + testdb 集成测试覆盖）
//   - GetStream 不做本地模拟，误用直接 panic 以暴露问题
//   - Close 空实现
package testmq

import (
	"context"
	"sync"

	"rednote/packages/mq"

	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/gorm"
)

// Client mq.MQClient 的内存模拟实现
type Client struct {
	mu   sync.Mutex
	sent []*mq.MQMessage
	err  error // 故障注入：非 nil 时 Send/SendTX 返回
}

var _ mq.MQClient = (*Client)(nil)

// New 创建内存模拟客户端
func New() *Client {
	return &Client{}
}

// Send 记录消息；注入错误时返回错误且不记录
func (c *Client) Send(_ context.Context, msg *mq.MQMessage) error {
	return c.record(msg)
}

// SendTX 记录消息（不写 outbox 表）；注入错误时返回错误且不记录
func (c *Client) SendTX(_ context.Context, msg *mq.MQMessage, _ *gorm.DB) error {
	return c.record(msg)
}

// GetStream 消费面不在本地模拟范围，误用直接 panic 以暴露问题
func (c *Client) GetStream() jetstream.JetStream {
	panic("testmq: GetStream 未实现（消费面不做本地模拟）")
}

// Close 空实现：内存模拟无资源可释放
func (c *Client) Close() {}

func (c *Client) record(msg *mq.MQMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.sent = append(c.sent, msg)
	return nil
}

// Sent 返回已发送消息快照（Send 与 SendTX 合并，按发送顺序）
func (c *Client) Sent() []*mq.MQMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*mq.MQMessage, len(c.sent))
	copy(out, c.sent)
	return out
}

// SentBySubject 返回指定主题的消息快照
func (c *Client) SentBySubject(subject string) []*mq.MQMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*mq.MQMessage, 0, len(c.sent))
	for _, m := range c.sent {
		if m.Subject == subject {
			out = append(out, m)
		}
	}
	return out
}

// Count 已发送消息数
func (c *Client) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

// Reset 清空消息记录并取消故障注入
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = nil
	c.err = nil
}

// SetSendError 故障注入：此后 Send/SendTX 返回该错误；传 nil 取消
func (c *Client) SetSendError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}
