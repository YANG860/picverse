package common

type Status int32

const (
	STATUS_PENDING Status = iota
	STATUS_PROCESSEING
	STATUS_DONE
	STATUS_CANCELED
	STATUS_FAILED
)

type OutBoxEntry struct {
	BaseModel

	TraceID string `gorm:"type:varchar(255);not null;index;comment:追踪ID" json:"trace_id"`

	Subject string `gorm:"type:varchar(255);not null;index;comment:主题" json:"subject"`
	SendAt  int64  `gorm:"type:bigint;not null;comment:发送时间" json:"send_at"`

	Header  map[string]string `gorm:"type:json;comment:头部信息" json:"header"`
	Payload []byte            `gorm:"type:bytea;not null;comment:负载数据" json:"payload"`

	Status     Status `gorm:"type:integer;not null;comment:状态" json:"status"`
	RetryCount int    `gorm:"type:integer;not null;default:0;comment:发布重试次数" json:"retry_count"`
}
