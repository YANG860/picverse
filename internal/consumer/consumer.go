package consumer

import (
	"context"
	"log/slog"
	"rednote/gen/query"
	"rednote/packages/mq"
	"rednote/packages/oss"

	"gorm.io/gorm"
)

type Consumer struct {
	DB *gorm.DB
	q  *query.Query

	MQ  mq.MQClient
	S3  oss.S3Repo
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
