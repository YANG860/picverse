package common

// ProcessedMessage 消费端消息幂等去重记录（共用机制，所有 AtLeastOnce 消费点复用）：
// 与业务副作用同事务登记（exactly-once effect），重复投递按 message_id 主键冲突跳过。
// TODO: 保留 7 天后清理（远超 ack_wait × max_deliver 重投窗口）；当前无定时巡检任务可挂载，
// 表稳态极小，暂不清理。
type ProcessedMessage struct {
	MessageID   int64  `gorm:"primaryKey;comment:MQMessage.ID(雪花ID)" json:"message_id"`
	Handler     string `gorm:"type:varchar(64);not null;comment:处理器标识" json:"handler"`
	ProcessedAt int64  `gorm:"not null;comment:处理时间(unix毫秒)" json:"processed_at"`
}
