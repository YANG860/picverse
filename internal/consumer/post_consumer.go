package consumer

import (
	"context"
	"fmt"
	"os"
	"rednote/config"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"
	postv1 "rednote/gen/go/post/v1"
	v1 "rednote/internal/post/v1"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/mq"
	"rednote/packages/oss"
	"rednote/packages/trace"
	"runtime/debug"
	"strconv"

	"github.com/bwmarrin/snowflake"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

type PostConsumer struct {
	Consumer
	Repo         v1.PostRepository
	MediaService mediav1connect.MediaServiceClient
	ConsumeCtx   jetstream.ConsumeContext
}

func NewPostConsumer(mqClient mq.MQClient, s3 oss.S3Repo, lf logger.LoggerFactory, cfg *config.Config, mediaService mediav1connect.MediaServiceClient, repo v1.PostRepository) *PostConsumer {
	ctx, cancel := context.WithCancel(context.Background())
	return &PostConsumer{
		Consumer: Consumer{
			name:       cfg.Consumer.Post.Name,
			maxRetry:   cfg.Consumer.Post.MaxRetry,
			workingDir: cfg.Consumer.Post.WorkingDir,

			MQ:  mqClient,
			S3:  s3,
			log: lf.GetLoggerOf("post_consumer"),

			ctx:    ctx,
			cancel: cancel,
		},
		Repo:         repo,
		MediaService: mediaService,
	}
}

func (c *PostConsumer) Start() {

	c.log.Info("starting post consumer...")
	_, err := os.Stat(c.workingDir)
	if os.IsNotExist(err) {
		err := os.MkdirAll(c.workingDir, 0755)
		if err != nil {
			c.log.Error("failed to create working directory", "error", err)
			panic(err)
		}
	}

	consumer, err := c.MQ.GetStream().CreateOrUpdateConsumer(c.ctx,
		defaults.PostStream,
		jetstream.ConsumerConfig{
			Name:    c.name,
			Durable: c.name,

			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckPolicy:     jetstream.AckExplicitPolicy,

			MaxDeliver: c.maxRetry,
		})
	if err != nil {
		c.log.Error("failed to create or update consumer", "error", err)
		panic(err)
	}

	consumeCtx, err := consumer.Consume(c.HandleMessage)
	if err != nil {
		panic(err)
	}
	c.ConsumeCtx = consumeCtx
	c.log.Info("post consumer started")
}

func (c *PostConsumer) HandleMessage(msg jetstream.Msg) {

	traceID := msg.Headers().Get(defaults.TraceIDKey)
	sendAt := msg.Headers().Get(defaults.SendAtKey)
	id := msg.Headers().Get(defaults.MessageIDKey)
	ctx := trace.WithTraceID(c.ctx, traceID)

	defer func() {
		if r := recover(); r != nil {
			c.log.Error("panic occurred",
				"trace_id", traceID,
				"send_at", sendAt,
				"id", id,
				"error", r,
				"stack", debug.Stack(),
			)
		}
	}()

	var err error
	var ack bool

	m := &postv1.PostMessage{}
	err = proto.Unmarshal(msg.Data(), m)
	if err != nil {
		_ = msg.Term()
		c.log.Error("failed to unmarshal post message",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
			"error", err,
		)
		return
	}

	switch message := m.Payload.(type) {
	case *postv1.PostMessage_AuditPost:
		ack, err = c.handleAuditPost(ctx, message.AuditPost)
	case *postv1.PostMessage_PublishPost:
		ack, err = c.handlePublishPost(ctx, message.PublishPost)
	case *postv1.PostMessage_BanPost:
		ack, err = c.handleBanPost(ctx, message.BanPost)
	case *postv1.PostMessage_DeletePost:
		ack, err = c.handleDeletePost(ctx, message.DeletePost)
	default:
		ack = true
		err = fmt.Errorf("unhandled post message")
	}

	if err != nil {
		c.log.Error("failed to handle post message",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
			"error", err,
		)
	} else {
		c.log.Info("successfully handled post message",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
		)
	}

	if ack {
		_ = msg.Ack()
	} else {
		_ = msg.Nak()
		c.log.Error("message nak",
			"id", id,
			"trace_id", traceID,
			"send_at", sendAt,
		)
	}

}

func (c *PostConsumer) Close() {
	c.cancel()
	c.ConsumeCtx.Stop()
}

func (c *PostConsumer) handleAuditPost(ctx context.Context, msg *postv1.AuditPost) (ack bool, err error) {
	postID, err := snowflake.ParseString(msg.PostId)
	if err != nil {
		return ACK, err
	}

	/* postDetail, err := c.Repo.GetPost(ctx, postID.Int64())
	if err != nil {
		return RETRY, err
	} */

	// TODO: audit post

	// TODO: check all media active
	pulishMsg := &postv1.PublishPost{
		PostId:   msg.PostId,
		AuthorId: msg.AuthorId,
	}

	return c.SetPostStatusActive(ctx, postID.Int64(), pulishMsg)
}

func (c *PostConsumer) SetPostStatusActive(ctx context.Context, postID int64, message *postv1.PublishPost) (ack bool, err error) {
	publishMsg := &postv1.PostMessage{
		Payload: &postv1.PostMessage_PublishPost{
			PublishPost: message,
		},
	}

	// 单事务内完成状态流转与发件箱写入：AUDITING→PUBLISHED 未命中（0 行）则不发消息
	err = c.Repo.Transaction(func(repo v1.PostRepository) error {
		mqMsg, err := mq.NewMQMessage(defaults.PostMessageStream, nil, publishMsg)
		if err != nil {
			return err
		}

		info, err := repo.SetPostStatus(ctx, postID,
			postv1.PostStatus_POST_STATUS_AUDITING,
			postv1.PostStatus_POST_STATUS_PUBLISHED,
		)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return nil
		}

		return c.MQ.SendTX(ctx, mqMsg, repo.DB())
	})

	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

// handlePublishPost 帖子发布后的下游处理。
// 目前无明确业务规范，占位留痕不阻塞 ACK（对齐 media 侧 HandlePublishMedia 模式）；
// 具体下游逻辑（动态流/通知等）待需求明确后补。
func (c *PostConsumer) handlePublishPost(ctx context.Context, msg *postv1.PublishPost) (ack bool, err error) {
	// TODO: 实现发布后的下游处理
	c.log.Info("post published",
		"post_id", msg.PostId,
	)
	return ACK, nil
}

// handleDeletePost 删帖消息的下游回收：同事务物理删除软删帖子行（仅 DELETED 行，见 repo.DeletePost）
// 与其媒体关联，并按被删关联逐槽位写 outbox 卸载消息（deref_media → media consumer 递减引用计数），
// 计数回收与删除同事务必达不漏；重复投递时 0 行命中即幂等成功（不重复发消息/减计数）。
func (c *PostConsumer) handleDeletePost(ctx context.Context, msg *postv1.DeletePost) (ack bool, err error) {
	postID, err := snowflake.ParseString(msg.PostId)
	if err != nil {
		return ACK, err
	}

	authorID, err := snowflake.ParseString(msg.AuthorId)
	if err != nil {
		return ACK, err
	}

	err = c.Repo.Transaction(func(repo v1.PostRepository) error {
		if _, err := repo.DeletePost(ctx, authorID.Int64(), postID.Int64()); err != nil {
			return err
		}

		assetIDs, err := repo.DeletePostMedias(ctx, postID.Int64())
		if err != nil {
			return err
		}

		// 逐槽位卸载消息与删除同事务写 outbox（SendTX），必达；每条消息唯一 ID，媒体侧幂等去重
		for _, assetID := range assetIDs {
			deref, err := mq.NewMQMessage(defaults.MediaMessageStream, nil, &mediav1.MediaMessage{
				Payload: &mediav1.MediaMessage_DerefMedia{
					DerefMedia: &mediav1.DerefMedia{AssetId: strconv.FormatInt(assetID, 10)},
				},
			})
			if err != nil {
				return err
			}
			if err := c.MQ.SendTX(ctx, deref, repo.DB()); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return RETRY, err
	}

	return ACK, nil
}

// handleBanPost 帖子封禁后的下游处理。
// 目前无明确业务规范，占位留痕不阻塞 ACK（对齐 media 侧 HandlePublishMedia 模式）；
// 具体下游逻辑（媒体资产处置/通知等）待需求明确后补。
func (c *PostConsumer) handleBanPost(ctx context.Context, msg *postv1.BanPost) (ack bool, err error) {
	// TODO: 实现封禁后的下游处理
	c.log.Info("post banned",
		"post_id", msg.PostId,
	)
	return ACK, nil
}
