package consumer

import (
	"context"
	"errors"
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

	"connectrpc.com/connect"
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
		newConsumer(cfg.Consumer.Post.Name, cfg.Consumer.Post.MaxRetry, cfg.Consumer.Post.WorkingDir, cfg.Consumer.HandleTimeout,
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

	// 发布前置校验：帖内媒体全部 ACTIVE 才可对外可见（在途 RETRY 等就绪，终态丢弃不发布）
	if err := c.checkAllMediaActive(ctx, postID.Int64()); err != nil {
		switch {
		case errors.Is(err, errMediaNotReady):
			// 重投窗口内等媒体就绪（JetStream 按 BackOff 调度）；窗口耗尽仍未就绪则判不可用
			// 回退 DRAFT——绝不能把 RETRY 留到 MaxDeliver 上限：JetStream 会静默丢弃消息（无死信），
			// 帖子将永久卡在 AUDITING。与 media_consumer 的耗尽兜底同口径。
			if DeliveredFrom(ctx) >= uint64(c.maxRetry) {
				return c.SetPostStatusDraft(ctx, postID.Int64(), err)
			}
			return RETRY, err
		case errors.Is(err, errMediaUnusable):
			// 媒体终态不可发布：回退 DRAFT 供作者修复后重新发布
			return c.SetPostStatusDraft(ctx, postID.Int64(), err)
		default:
			return classifyErr(err), err
		}
	}

	pulishMsg := &postv1.PublishPost{
		PostId:   msg.PostId,
		AuthorId: msg.AuthorId,
	}

	return c.SetPostStatusActive(ctx, postID.Int64(), pulishMsg)
}

// 媒体就绪性检查哨兵错误（见 checkAllMediaActive），handleAuditPost 据此决定 Ack/Nak
var (
	// errMediaNotReady 媒体在途（UPLOADING/AUDITING/PROCESSING）：瞬时，Nak 重投等就绪后发布
	errMediaNotReady = errors.New("media assets not ready")
	// errMediaUnusable 媒体终态（FAILED/BANNED/ERROR/DELETED/UNSPECIFIED 或资产查无）：永不 ACTIVE，不发布
	errMediaUnusable = errors.New("media assets unusable")
)

// checkAllMediaActive 发布前置校验：帖子关联的全部媒体资产必须 ACTIVE（变体产出完成）才允许发布。
// 按“终态白名单”判定，而非列举在途态：只有列出的终态才判不可用（ACK + 回退 DRAFT），
// 其余（在途 + 未来新增状态）一律按未就绪保守重试（有重投窗口兑底，不会无限等）：
//   - ACTIVE：就绪；
//   - 终态 FAILED/ERROR/BANNED/DELETED/UNSPECIFIED、资产查无、槽位数据残缺：永不 ACTIVE → errMediaUnusable；
//   - 其他（UPLOADING/AUDITING/PROCESSING 及未知值）：瞬时/不确定 → errMediaNotReady。
//
// 不枚举在途态是刻意选择：media 侧新增一个在途状态时，落 default 就重试（正确行为），
// 而列举写法会把它误判为终态并直接回退草稿（沉默的功能回归）。
//
// 无媒体关联的帖子直接放行；依赖故障（DB/RPC）与契约破坏（结果缺项）原样返回，由调用方分类（RETRY）。
func (c *PostConsumer) checkAllMediaActive(ctx context.Context, postID int64) error {
	medias, err := c.Repo.GetPostMedias(ctx, []int64{postID})
	if err != nil {
		return fmt.Errorf("post.GetPostMedias failed: %w", err)
	}

	// 槽位 → 资产去重（同一资产多槽位只核验一次）
	ids := make([]string, 0, len(medias))
	seen := make(map[int64]struct{}, len(medias))
	for _, m := range medias {
		if m == nil || m.Media == nil || m.Media.AssetID == 0 {
			// 槽位数据残缺无法核验，且不会自行恢复：按终态处置
			return fmt.Errorf("post %d media slot missing asset: %w", postID, errMediaUnusable)
		}
		if _, ok := seen[m.Media.AssetID]; ok {
			continue
		}
		seen[m.Media.AssetID] = struct{}{}
		ids = append(ids, strconv.FormatInt(m.Media.AssetID, 10))
	}
	if len(ids) == 0 {
		return nil
	}

	resp, err := c.MediaService.GetMediaAssets(ctx, connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: ids}))
	if err != nil {
		return fmt.Errorf("media.GetMediaAssets failed: %w", err)
	}

	// 结果按 id 归位（服务端逐项回填请求 id）；缺项视为契约破坏，瞬时重投等正确结果
	got := make(map[string]*mediav1.MediaAssetResult, len(resp.Msg.GetResults()))
	for _, r := range resp.Msg.GetResults() {
		got[r.GetId()] = r
	}

	for _, id := range ids {
		r, ok := got[id]
		if !ok {
			return fmt.Errorf("media.GetMediaAssets missing result for asset %s", id)
		}
		if r.GetFail() || r.GetAsset() == nil {
			// 资产查无/ID 非法：终态，重投只会得到同样裁决
			return fmt.Errorf("media asset %s unavailable: %w", id, errMediaUnusable)
		}

		status := r.GetAsset().GetStatus()
		switch status {
		case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE:
			// 就绪，继续核验下一资产
		case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ERROR,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_DELETED,
			mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UNSPECIFIED:
			return fmt.Errorf("media asset %s status %s: %w", id, status, errMediaUnusable)
		default:
			// 在途（UPLOADING/AUDITING/PROCESSING）与未来新增状态：保守按未就绪重试
			return fmt.Errorf("media asset %s status %s: %w", id, status, errMediaNotReady)
		}
	}
	return nil
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

// SetPostStatusDraft 媒体不可用回退草稿：条件更新 AUDITING→DRAFT（0 行即已非 AUDITING，幂等），
// 让作者修复后重新发布；单条条件更新无需事务，无消息发送。
//
// 回退成功返回 ACK + nil：这是**预期业务裁决**（不可发布），不是处理失败——reason 只记 Warn
// 带 kv 留痕。若随 ACK 返回 error，消息循环会把它当成处理失败记 ERROR，污染告警通道。
// 回退本身失败才是真错误（RETRY 重投，避免误丢状态）。
func (c *PostConsumer) SetPostStatusDraft(ctx context.Context, postID int64, reason error) (ack bool, err error) {
	info, err := c.Repo.SetPostStatus(ctx, postID,
		postv1.PostStatus_POST_STATUS_AUDITING,
		postv1.PostStatus_POST_STATUS_DRAFT,
	)
	if err != nil {
		return RETRY, fmt.Errorf("revert post to draft failed: %w", err)
	}

	c.log.Warn("post publish rejected, revert to draft",
		"post_id", postID, "rows", info.RowsAffected, "reason", reason)
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

// handleDeletePost 删帖消息的下游回收（同事务）：物理删除软删帖子行（仅 DELETED 行，见 repo.DeletePost）
// 及其下级明细——媒体关联、评论行、点赞/收藏明细（任意状态一并清；计数随帖子行消失无需回收）——
// 并按被删关联逐槽位写 outbox 卸载消息（deref_media → media consumer 递减引用计数）。
// 级联回收与删除同事务必达不漏；重复投递时 0 行命中即幂等成功（不重复发消息/减计数）。
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

		// 连带物理回收该帖评论行（软删/正常一并清；帖子行已删，comment_count 随之消失无需回收）；
		// 重复投递 0 行命中即幂等
		cmtInfo, err := repo.DeletePostComments(ctx, postID.Int64())
		if err != nil {
			return err
		}
		c.log.Info("delete post comments reclaim", "post_id", msg.GetPostId(), "rows", cmtInfo.RowsAffected)

		// 级联回收点赞/收藏明细：计数随帖子行消失，但明细行必须清——
		// 否则孤儿行会被“我点赞/收藏的帖子”查询命中并指向不存在的帖子
		likeInfo, err := repo.DeletePostLikes(ctx, postID.Int64())
		if err != nil {
			return err
		}
		favInfo, err := repo.DeletePostFavourites(ctx, postID.Int64())
		if err != nil {
			return err
		}
		c.log.Info("delete post interactions reclaim", "post_id", msg.GetPostId(),
			"likes", likeInfo.RowsAffected, "favourites", favInfo.RowsAffected)

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
