package consumer

import (
	"fmt"
	"strconv"

	"github.com/bwmarrin/snowflake"
)

// 语义约定（错误分类决定 Ack/Nak）：ACK = 非法数据/不可恢复，丢弃不重投；RETRY = 瞬时错误，Nak 重投。
// 解析工具的错误即携带分类语义：ParseMessageID 失败 RETRY（消息头损坏，重投等正确消息），
// ParseSnowflakeID 失败 ACK（业务 ID 非法，不可恢复）。

// ParseMessageID 解析消息头 MessageID（雪花十进制）。
// 失败 = 消息头缺失/损坏：不登记不处理，调用方应 RETRY 等正确消息（宁可重试不可错登记）
func ParseMessageID(id string) (int64, error) {
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid message id %q: %w", id, err)
	}
	return v, nil
}

// ParseSnowflakeID 解析业务 ID（雪花字符串，field 为字段名用于错误上下文）。
// 失败 = 非法数据不可恢复：调用方应 ACK 丢弃
func ParseSnowflakeID(s, field string) (snowflake.ID, error) {
	v, err := snowflake.ParseString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", field, s, err)
	}
	return v, nil
}
