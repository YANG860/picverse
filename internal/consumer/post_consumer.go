package consumer

import (
	"context"
	"fmt"
	"rednote/config"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"
	postv1 "rednote/gen/go/post/v1"
	v1 "rednote/internal/post/v1"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/mq"
	"strconv"
)

type PostConsumer struct {
	*MessageLoop[*postv1.PostMessage]
	Repo         v1.PostRepository
	MediaService mediav1connect.MediaServiceClient
}

func NewPostConsumer(mqClient mq.MQClient, lf logger.LoggerFactory, cfg *config.Config, mediaService mediav1connect.MediaServiceClient, repo v1.PostRepository) *PostConsumer {
	c := &PostConsumer{
		Repo:         repo,
		MediaService: mediaService,
	}
	c.MessageLoop = NewMessageLoop(
		newConsumer(cfg.Consumer.Post.Name, cfg.Consumer.Post.MaxRetry, cfg.Consumer.Post.WorkingDir,
			mqClient, lf, "post_consumer"),
		defaults.PostStream,
		func() *postv1.PostMessage { return &postv1.PostMessage{} },
		c.dispatch,
	)
	return c
}

// dispatch post 消息分发（oneof → 业务 handler）；错误分类语义见 utils.go
func (c *PostConsumer) dispatch(ctx context.Context, m *postv1.PostMessage, _ string) (ack bool, err error) {
	switch message := m.Payload.(type) {
	case *postv1.PostMessage_AuditPost:
		return c.handleAuditPost(ctx, message.AuditPost)
	case *postv1.PostMessage_PublishPost:
		return c.handlePublishPost(ctx, message.PublishPost)
	case *postv1.PostMessage_BanPost:
		return c.handleBanPost(ctx, message.BanPost)
	case *postv1.PostMessage_DeletePost:
		return c.handleDeletePost(ctx, message.DeletePost)
	default:
		return ACK, fmt.Errorf("unhandled post message")
	}
}

func (c *PostConsumer) handleAuditPost(ctx context.Context, msg *postv1.AuditPost) (ack bool, err error) {
	postID, err := ParseSnowflakeID(msg.PostId, "post_id")
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
	postID, err := ParseSnowflakeID(msg.PostId, "post_id")
	if err != nil {
		return ACK, err
	}

	authorID, err := ParseSnowflakeID(msg.AuthorId, "author_id")
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
		c.log.Info("delete post reclaim", "post_id", msg.GetPostId(), "asset_ids", assetIDs)

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
