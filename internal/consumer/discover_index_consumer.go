package consumer

import (
	"context"
	"fmt"
	"rednote/config"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	commonv1 "rednote/internal/common/v1"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/meili"
	"rednote/packages/mq"

	"connectrpc.com/connect"
	"gorm.io/gorm"
)

// DiscoverIndexConsumer 搜推索引消费者：消费 post 流消息维护 Meilisearch posts 索引（P3）。
// 与 FanoutConsumer 拆为独立 durable（失败隔离：各自重试预算/死信/进度，见 doc/internal/discover/discover.md）。
type DiscoverIndexConsumer struct {
	*MessageLoop[*postv1.PostMessage]
	Meili       meili.MeiliRepo
	PostService postv1connect.PostServiceClient
	Dedup       commonv1.ProcessedMessageRepository
}

// NewDiscoverIndexConsumer 创建索引同步消费者（消费 post 流）
func NewDiscoverIndexConsumer(mqClient mq.MQClient, lf logger.LoggerFactory, cfg *config.Config,
	meiliClient meili.MeiliRepo, postService postv1connect.PostServiceClient,
	dedup commonv1.ProcessedMessageRepository) *DiscoverIndexConsumer {
	// 启动即幂等应用 posts 索引 settings（searchable/sortable）：索引可能由 UpsertPost 隐式创建
	// （无 settings），不应用的话「最新」排序（sort by created_at/post_id）会被 Meili 拒绝（internal error）。
	// 失败仅告警不阻断启动：索引写入路径自带建索引兜底，下次重启会重试应用。
	ensureCtx, cancel := context.WithTimeout(context.Background(), cfg.Consumer.HandleTimeout)
	defer cancel()
	if err := meiliClient.EnsurePostsIndex(ensureCtx); err != nil {
		panic(fmt.Errorf("ensure posts index settings failed（「最新」排序可能不可用）: %w", err))
	}
	c := &DiscoverIndexConsumer{
		Meili:       meiliClient,
		PostService: postService,
		Dedup:       dedup,
	}
	c.MessageLoop = NewMessageLoop(
		newConsumer(cfg.Consumer.Discover.Index.Name, cfg.Consumer.Discover.Index.MaxRetry, "", cfg.Consumer.HandleTimeout,
			mqClient, lf, "discover_index_consumer"),
		defaults.PostStream,
		func() *postv1.PostMessage { return &postv1.PostMessage{} },
		c.dispatch,
	)
	return c
}

// dispatch post 流消息分发（oneof → 索引 handler）；错误分类语义见 utils.go
func (c *DiscoverIndexConsumer) dispatch(ctx context.Context, m *postv1.PostMessage, messageID string) (ack bool, err error) {
	switch message := m.Payload.(type) {
	case *postv1.PostMessage_PublishPost:
		return c.handleUpsertPostIndex(ctx, message.PublishPost, messageID)
	case *postv1.PostMessage_BanPost:
		return c.handleRemovePostIndex(ctx, message.BanPost.PostId, messageID)
	case *postv1.PostMessage_DeletePost:
		return c.handleRemovePostIndex(ctx, message.DeletePost.PostId, messageID)
	case *postv1.PostMessage_AuditPost:
		// 审核中间态不进搜索索引（仅 PUBLISHED 进索引），直接确认
		return ACK, nil
	default:
		return ACK, fmt.Errorf("unhandled post message")
	}
}

func (c *DiscoverIndexConsumer) handleUpsertPostIndex(ctx context.Context, msg *postv1.PublishPost, messageID string) (ack bool, err error) {
	msgID, err := ParseMessageID(messageID)
	if err != nil {
		// message_id 缺失/非法：不登记不写索引，RETRY 等正确消息（宁可重试不可错登记）
		return RETRY, err
	}
	postID := msg.GetPostId()
	if _, err := ParseSnowflakeID(postID, "post_id"); err != nil {
		// 非法 post ID 不可恢复，ACK 丢弃（与 PostConsumer 各 handler 一致）
		return ACK, err
	}

	// 拉取帖子组装文档（发布时刻实体即 PUBLISHED）
	resp, err := c.PostService.GetPostsByIDs(ctx, connect.NewRequest(&postv1.GetPostsByIDsRequest{
		PostIds: []string{postID},
	}))
	if err != nil {
		return classifyErr(err), fmt.Errorf("post.GetPostsByIDs failed: %w", err)
	}

	var post *postv1.Post
	for _, r := range resp.Msg.GetResults() {
		// 返回不保证顺序，按 Key（post_id）定位
		if r.GetPostId() != postID {
			continue
		}
		if r.GetFail() || r.GetPost() == nil {
			c.log.Warn("post result unavailable，降级跳过索引",
				"id", messageID, "post_id", postID, "error", r.GetErr().GetMsg())
			return ACK, nil
		}
		post = r.GetPost()
	}
	if post == nil || post.GetStatus() != postv1.PostStatus_POST_STATUS_PUBLISHED {
		// 状态与发布消息不一致（并发删除/封禁或乱序投递）：不进索引
		c.log.Warn("post not published，降级跳过索引",
			"id", messageID, "post_id", postID, "status", post.GetStatus())
		return ACK, nil
	}

	// 组装索引文档（字段从 post 实体提取，文档组装收敛在 meili.NewPostIndexDoc）
	doc, docErr := meili.NewPostIndexDoc(postID, postIndexFields(post))
	if docErr != nil {
		// created_at 非法等数据异常：降级（created_at=0）继续索引，不阻塞链路
		c.log.Warn("post index doc degraded", "id", messageID, "post_id", postID, "error", docErr)
	}

	// 幂等执行（共用去重机制）：去重登记与索引写入同事务，重复投递跳过
	applied, err := c.Dedup.ExecuteIdempotent(ctx, msgID, "upsert_post_index", func(_ *gorm.DB) error {
		return c.Meili.UpsertPost(ctx, doc)
	})
	if err != nil {
		return RETRY, err
	}
	if !applied {
		c.log.Info("post index upsert skipped duplicate", "id", messageID, "post_id", postID)
		return ACK, nil
	}
	return ACK, nil
}

// handleRemovePostIndex 软删/封禁 → 删除 posts 索引文档（同步丢失窗口由 BFF 命中后回查过滤兜底）。
// 幂等双保险：Dedup.ExecuteIdempotent + 按主键删除（删不存在的键为 no-op）。
func (c *DiscoverIndexConsumer) handleRemovePostIndex(ctx context.Context, postID, messageID string) (ack bool, err error) {
	msgID, err := ParseMessageID(messageID)
	if err != nil {
		// message_id 缺失/非法：不登记不删索引，RETRY 等正确消息
		return RETRY, err
	}
	if _, err := ParseSnowflakeID(postID, "post_id"); err != nil {
		// 非法 post ID 不可恢复，ACK 丢弃
		return ACK, err
	}

	applied, err := c.Dedup.ExecuteIdempotent(ctx, msgID, "delete_post_index", func(_ *gorm.DB) error {
		return c.Meili.RemovePost(ctx, postID)
	})
	if err != nil {
		return RETRY, err
	}
	if !applied {
		c.log.Info("post index delete skipped duplicate", "id", messageID, "post_id", postID)
		return ACK, nil
	}
	return ACK, nil
}

// postIndexFields post 实体 → 索引字段集合（proto → map 提取的薄编排，
// 索引文档组装/类型降级收敛在 meili.NewPostIndexDoc）。
func postIndexFields(p *postv1.Post) map[string]any {
	return map[string]any{
		"title":           p.GetTitle(),
		"full_text":       p.GetFullText(),
		"author_id":       p.GetAuthorId(),
		"created_at":      p.GetCreatedAt(),
		"like_count":      p.GetPostStat().GetLikeCount(),
		"favourite_count": p.GetPostStat().GetFavouriteCount(),
		"comment_count":   p.GetPostStat().GetCommentCount(),
		"view_count":      p.GetPostStat().GetViewCount(),
	}
}
