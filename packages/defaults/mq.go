package defaults

import "time"

const (
	MessageIDKey = "message_id"
	SendAtKey    = "send_at"

	MaxRetry = 5
)

// asyncPublishAckTimeout 是异步发布等待 ack 的上限，超时后 future.Err() 收到 ErrAsyncPublishTimeout
const AsyncPublishAckTimeout = 5 * time.Second
