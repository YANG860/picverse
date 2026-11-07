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
	"rednote/packages/trace"
	"runtime/debug"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/bwmarrin/snowflake"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// DiscoverConsumer 搜推消费者：消费 post 流消息，驱动 Meilisearch 索引同步（P3，
// ）；关注流扇出（P1）后续共用本消费者。
// 消费机制与 PostConsumer 一致：ACK/NAK 确认，可重试错误 Nak 重投，不可恢复 Term。
type DiscoverConsumer struct {
	Consumer
	Meili       meili.MeiliRepo
	PostService postv1connect.PostServiceClient
	Dedup       commonv1.ProcessedMessageRepository
	ConsumeCtx  jetstream.ConsumeContext
}

// NewDiscoverConsumer 创建 DiscoverConsumer
func NewDiscoverConsumer(mqClient mq.MQClient, lf logger.LoggerFactory, cfg *config.Config,
	meiliClient meili.MeiliRepo, postService postv1connect.PostServiceClient,
	dedup commonv1.ProcessedMessageRepository) *DiscoverConsumer {
	ctx, cancel := context.WithCancel(context.Background())
	return &DiscoverConsumer{
		Consumer: Consumer{
			name:     cfg.Consumer.Discover.Name,
			maxRetry: cfg.Consumer.Discover.MaxRetry,

			MQ:     mqClient,
			log:    lf.GetLoggerOf("discover_consumer"),
			ctx:    ctx,
			cancel: cancel,
		},
		Meili:       meiliClient,
		PostService: postService,
		Dedup:       dedup,
	}
}

// Start 启动消费循环（建立 durable consumer 消费 post 流）
func (c *DiscoverConsumer) Start() {

	c.log.Info("starting discover consumer...")

	// 启动即确保存在 posts 索引并应用设置（fail-fast：Meili 配置/权限问题早暴露）
	if err := c.Meili.EnsurePostsIndex(c.ctx); err != nil {
		c.log.Error("failed to ensure posts index", "error", err)
		panic(err)
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
	c.log.Info("discover consumer started")
}

// HandleMessage 消息分发：解析 PostMessage 按 oneof payload 路由到索引同步处理器
func (c *DiscoverConsumer) HandleMessage(msg jetstream.Msg) {

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

	msgType := strings.TrimPrefix(fmt.Sprintf("%T", m.Payload), "*postv1.PostMessage_")
	switch message := m.Payload.(type) {
	case *postv1.PostMessage_PublishPost:
		ack, err = c.handleUpsertPostIndex(ctx, message.PublishPost, id)
	case *postv1.PostMessage_BanPost:
		ack, err = c.handleRemovePostIndex(ctx, message.BanPost.PostId, id)
	case *postv1.PostMessage_DeletePost:
		ack, err = c.handleRemovePostIndex(ctx, message.DeletePost.PostId, id)
	case *postv1.PostMessage_AuditPost:
		// 审核中间态不进搜索索引（仅 PUBLISHED 进索引），直接确认
		ack = true
	default:
		ack = true
		err = fmt.Errorf("unhandled post message")
	}

	if err != nil {
		c.log.Error("failed to handle post message",
			"id", id,
			"type", msgType,
			"trace_id", traceID,
			"send_at", sendAt,
			"error", err,
		)
	} else {
		c.log.Info("successfully handled post message",
			"id", id,
			"type", msgType,
			"trace_id", traceID,
			"send_at", sendAt,
		)
	}

	// Ack/Nak 失败不吞错：Ack 失败会导致消息重投（AtLeastOnce），需可追溯
	if ack {
		if ackErr := msg.Ack(); ackErr != nil {
			c.log.Warn("message ack failed，将重投", "id", id, "error", ackErr)
		}
	} else if nakErr := msg.Nak(); nakErr != nil {
		c.log.Warn("message nak failed", "id", id, "error", nakErr)
	}
}

// Close 停止消费
func (c *DiscoverConsumer) Close() {
	c.cancel()
	if c.ConsumeCtx != nil {
		c.ConsumeCtx.Stop()
	}
}

// handleUpsertPostIndex 发布 → 拉取帖子组装文档并 upsert posts 索引。
// 幂等双保险：Dedup.ExecuteIdempotent（msgID 去重）+ AddDocuments 按主键覆盖；
// 仅 PUBLISHED 进索引，状态不一致（并发删除/封禁或乱序投递）降级跳过。
func (c *DiscoverConsumer) handleUpsertPostIndex(ctx context.Context, msg *postv1.PublishPost, messageID string) (ack bool, err error) {
	msgID, parseErr := strconv.ParseInt(messageID, 10, 64)
	if parseErr != nil {
		// message_id 缺失/非法：不登记不写索引，RETRY 等正确消息（宁可重试不可错登记）
		return RETRY, fmt.Errorf("invalid message id %q: %w", messageID, parseErr)
	}
	postID := msg.GetPostId()
	if _, parseErr := snowflake.ParseString(postID); parseErr != nil {
		// 非法 post ID 不可恢复，ACK 丢弃（与 PostConsumer 各 handler 一致）
		return ACK, fmt.Errorf("invalid post id %q: %w", postID, parseErr)
	}

	// 拉取帖子组装文档（发布时刻实体即 PUBLISHED）
	resp, err := c.PostService.GetPostsByIDs(ctx, connect.NewRequest(&postv1.GetPostsByIDsRequest{
		PostIds: []string{postID},
	}))
	if err != nil {
		return RETRY, fmt.Errorf("post.GetPostsByIDs failed: %w", err)
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

	// 幂等执行模板（共用去重机制）：去重登记与索引写入同事务，重复投递跳过
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
func (c *DiscoverConsumer) handleRemovePostIndex(ctx context.Context, postID, messageID string) (ack bool, err error) {
	msgID, parseErr := strconv.ParseInt(messageID, 10, 64)
	if parseErr != nil {
		// message_id 缺失/非法：不登记不删索引，RETRY 等正确消息
		return RETRY, fmt.Errorf("invalid message id %q: %w", messageID, parseErr)
	}
	if _, parseErr := snowflake.ParseString(postID); parseErr != nil {
		// 非法 post ID 不可恢复，ACK 丢弃
		return ACK, fmt.Errorf("invalid post id %q: %w", postID, parseErr)
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
// created_at 保持 unix 毫秒字符串（全局约定），由 meili 侧解析降级
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
