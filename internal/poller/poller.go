package poller

import (
	"context"
	"log/slog"
	"rednote/config"
	common "rednote/internal/common/models"
	commonv1 "rednote/internal/common/v1"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	mq "rednote/packages/mq"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/gorm"
)

type OutBoxPoller struct {
	DB *gorm.DB
	MQ mq.MQClient

	Repo commonv1.OutBoxRepository

	log *slog.Logger
	wg  sync.WaitGroup

	workers        int
	interval       time.Duration
	publishTimeout time.Duration

	ticker    *time.Ticker
	batchSize int

	ctx    context.Context
	cancel context.CancelFunc
}

func NewPoller(cfg *config.Config, db *gorm.DB, mq mq.MQClient, lf logger.LoggerFactory, repo commonv1.OutBoxRepository) *OutBoxPoller {
	ctx, cancel := context.WithCancel(context.Background())
	return &OutBoxPoller{
		DB:   db,
		MQ:   mq,
		Repo: repo,

		log: lf.GetLoggerOf("poller"),
		wg:  sync.WaitGroup{},

		workers:        cfg.Poller.Workers,
		interval:       cfg.Poller.Interval,
		publishTimeout: cfg.Poller.PublishTimeout,
		batchSize:      cfg.Poller.BatchSize,

		ticker: time.NewTicker(cfg.Poller.Interval),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (p *OutBoxPoller) Start() {
	p.log.InfoContext(p.ctx, "poller starting")
	for i := 0; i < p.workers; i++ {
		p.wg.Go(p.Work)
	}
	/* go func() {
		for range time.NewTicker(10 * time.Second).C {
			p.log.Info("poller  running")
		}
	}() */
}

func (p *OutBoxPoller) Close() {
	p.log.InfoContext(p.ctx, "poller stopping")
	// 先取消 context 让 worker 退出循环，再停 ticker，最后等 worker 收尾
	p.cancel()
	p.ticker.Stop()
	p.wg.Wait()
}

func (p *OutBoxPoller) Work() {

	defer func() {
		if r := recover(); r != nil {
			p.log.Error("panic recovered", "level", "fatal", "error", r, "stack", debug.Stack())
		}
	}()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.ticker.C:
		}

		affected := 0
		err := p.DB.Transaction(func(tx *gorm.DB) error {

			entries, err := p.Repo.WithTX(tx).Poll(p.ctx, p.batchSize)
			if err != nil {
				return err
			}
			affected = len(entries)

			successes, failures := p.dispatch(p.ctx, entries)

			err = p.Repo.WithTX(tx).AddEntryRetryCount(p.ctx, failures)
			if err != nil {
				return err
			}

			err = p.Repo.WithTX(tx).SetEntryStatusDone(p.ctx, successes)
			if err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			p.log.Error("failed to process entries", "error", err)
		} else if affected > 0 {
			p.log.Info("entries processed successfully", "count", affected)
		}
	}
}

func (p *OutBoxPoller) sendMsg(ctx context.Context, msgEntry *common.OutBoxEntry) error {
	// ctx 已取消（关闭或超时）时不再发出新消息，避免关闭期间产生新的 in-flight 消息
	if err := ctx.Err(); err != nil {
		return err
	}

	headers := nats.Header{}
	traceID := msgEntry.TraceID
	if traceID != "" {
		headers.Set(defaults.TraceIDKey, traceID)
	}
	headers.Set(defaults.SendAtKey, strconv.FormatInt(msgEntry.SendAt, 10))

	id := msgEntry.ID
	headers.Set(defaults.MessageIDKey, strconv.FormatInt(id, 10))

	for k, v := range msgEntry.Header {
		headers.Set(k, v)
	}

	future, err := p.MQ.GetStream().PublishMsgAsync(&nats.Msg{
		Subject: msgEntry.Subject,
		Header:  headers,
		Data:    msgEntry.Payload,
	},
		// MsgID 与 Send 直发路径对齐：broker 端去重，堵发布重试双发
		jetstream.WithMsgID(strconv.FormatInt(id, 10)),
	)

	if err != nil {
		return err
	}

	// 消息已发出，必须等待 ack future 的真实结果，不能因 ctx 超时提前返回：
	select {
	case err := <-future.Err():
		return err
	case <-future.Ok():
		return nil
	}
}

func (p *OutBoxPoller) dispatch(ctx context.Context, entries []*common.OutBoxEntry) (successes []int64, failures []int64) {

	type result struct {
		id  int64
		err error
	}

	results := make(chan result, len(entries))
	ctx, cancel := context.WithTimeout(p.ctx, p.publishTimeout)
	wg := sync.WaitGroup{}

	for _, e := range entries {
		wg.Go(func() {
			err := p.sendMsg(ctx, e)
			results <- result{
				id:  e.ID,
				err: err,
			}
		})
	}

	defer cancel()
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			p.log.Error("failed to publish message", "id", result.id, "error", result.err)
			failures = append(failures, result.id)
		} else {
			p.log.Info("message published successfully", "id", result.id)
			successes = append(successes, result.id)
		}
	}

	return
}
