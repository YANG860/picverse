package consumer

import (
	"context"
	"fmt"
	"rednote/config"
	commonpb "rednote/gen/go/common/v1"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	userv1 "rednote/gen/go/user/v1"
	userv1connect "rednote/gen/go/user/v1/userv1connect"
	commonv1 "rednote/internal/common/v1"
	discoverv1 "rednote/internal/discover/v1"
	"rednote/packages/cache"
	"rednote/packages/defaults"
	"rednote/packages/logger"
	"rednote/packages/mq"
	"strconv"

	"connectrpc.com/connect"
	"gorm.io/gorm"
)

// FanoutConsumer 关注流扇出消费者：消费 post 流发布消息，推拉分流——
// 大V写发件箱缓存（pull 供给）、普通作者扇出粉丝收件箱。
// 与 DiscoverIndexConsumer 拆为独立 durable（失败隔离：各自重试预算/死信/进度，见 doc/internal/discover/discover.md）。
type FanoutConsumer struct {
	*MessageLoop[*postv1.PostMessage]
	PostService postv1connect.PostServiceClient
	UserService userv1connect.UserServiceClient
	Fanout      discoverv1.FanoutRepo
	Timeline    cache.AuthorTimelineCache
	Dedup       commonv1.ProcessedMessageRepository

	fanoutCap int64 // 大V晋升线（cfg.BigV.PromoteFollowers）：粉丝数 ≥ 该值写缓存走 pull，否则扇出
}

// NewFanoutConsumer 创建关注流扇出消费者（消费 post 流）
func NewFanoutConsumer(mqClient mq.MQClient, lf logger.LoggerFactory, cfg *config.Config,
	postService postv1connect.PostServiceClient, userService userv1connect.UserServiceClient,
	fanout discoverv1.FanoutRepo, timeline cache.AuthorTimelineCache,
	dedup commonv1.ProcessedMessageRepository) *FanoutConsumer {
	fanoutCap := cfg.BigV.PromoteFollowers
	if fanoutCap <= 0 {
		fanoutCap = defaults.BigVPromoteFollowers
	}
	c := &FanoutConsumer{
		PostService: postService,
		UserService: userService,
		Fanout:      fanout,
		Timeline:    timeline,
		Dedup:       dedup,
		fanoutCap:   fanoutCap,
	}
	c.MessageLoop = NewMessageLoop(
		newConsumer(cfg.Consumer.Discover.Fanout.Name, cfg.Consumer.Discover.Fanout.MaxRetry, "",
			mqClient, lf, "fanout_consumer"),
		defaults.PostStream,
		func() *postv1.PostMessage { return &postv1.PostMessage{} },
		c.dispatch,
	)
	return c
}

// dispatch post 流消息分发（oneof → 扇出 handler）；错误分类语义见 utils.go。
// 扇出只关心发布事件（删帖不级联收件箱，由保留期回收，见 discover.md），其余直接确认
func (c *FanoutConsumer) dispatch(ctx context.Context, m *postv1.PostMessage, messageID string) (ack bool, err error) {
	switch message := m.Payload.(type) {
	case *postv1.PostMessage_PublishPost:
		return c.handlePublishFanout(ctx, message.PublishPost, messageID)
	case *postv1.PostMessage_AuditPost, *postv1.PostMessage_BanPost, *postv1.PostMessage_DeletePost:
		return ACK, nil
	default:
		return ACK, fmt.Errorf("unhandled post message")
	}
}

func (c *FanoutConsumer) handlePublishFanout(ctx context.Context, msg *postv1.PublishPost, messageID string) (ack bool, err error) {
	msgID, err := ParseMessageID(messageID)
	if err != nil {
		// message_id 缺失/非法：不登记不扇出，RETRY 等正确消息（宁可重试不可错登记）
		return RETRY, err
	}
	postID := msg.GetPostId()
	postUID, err := ParseSnowflakeID(postID, "post_id")
	if err != nil {
		return ACK, err
	}
	authorID := msg.GetAuthorId()
	authorUID, err := ParseSnowflakeID(authorID, "author_id")
	if err != nil {
		return ACK, err
	}

	// 取帖（发布时刻实体即 PUBLISHED；状态不一致降级跳过，与索引同步同口径）
	resp, err := c.PostService.GetPostsByIDs(ctx, connect.NewRequest(&postv1.GetPostsByIDsRequest{
		PostIds: []string{postID},
	}))
	if err != nil {
		return RETRY, fmt.Errorf("post.GetPostsByIDs failed: %w", err)
	}
	var post *postv1.Post
	for _, r := range resp.Msg.GetResults() {
		if r.GetPostId() != postID {
			continue
		}
		if r.GetFail() || r.GetPost() == nil {
			c.log.Warn("post result unavailable，降级跳过扇出", "id", messageID, "post_id", postID)
			return ACK, nil
		}
		post = r.GetPost()
	}
	if post == nil || post.GetStatus() != postv1.PostStatus_POST_STATUS_PUBLISHED {
		c.log.Warn("post not published，降级跳过扇出", "id", messageID, "post_id", postID)
		return ACK, nil
	}
	// created_at 非法降级 0（与索引同口径：不阻塞链路）
	createdAt, _ := strconv.ParseInt(post.GetCreatedAt(), 10, 64)

	// 粉丝数（分流判定输入）；取帖/取数间粉丝激增由扇出拉取的安全阀兑底
	profResp, err := c.UserService.GetUserProfile(ctx, connect.NewRequest(&userv1.GetUserProfileRequest{Uid: authorID}))
	if err != nil {
		return RETRY, fmt.Errorf("user.GetUserProfile failed: %w", err)
	}
	if profResp.Msg.GetFail() {
		return RETRY, fmt.Errorf("user.GetUserProfile fail: %s", profResp.Msg.GetErr().GetMsg())
	}
	followers := profResp.Msg.GetProfile().GetStat().GetFollowers()

	applied, err := c.Dedup.ExecuteIdempotent(ctx, msgID, "fanout_publish", func(_ *gorm.DB) error {
		return c.dispatchPublishFanout(ctx, postUID.Int64(), authorUID.Int64(), createdAt, followers)
	})
	if err != nil {
		return RETRY, err
	}
	if !applied {
		c.log.Info("fanout skipped duplicate", "id", messageID, "post_id", postID)
	}
	return ACK, nil
}

// dispatchPublishFanout 发布分流主体（含防抖动，见 discover.md）：
// ① 粉丝 ≥ 晋升线：设/刷大V标识（mark_ttl 保持窗）→ 写发件箱缓存；
// ② 粉丝 < 线但标识在（抖动保持期）：仍写缓存保供给连续（不刷新 TTL，自然过期回归扇出）；
// ③ 粉丝 < 线且无标识：扇出收件箱。
// 抖动/首帖双写由归并 post_id 去重兜底（宁重不漏）
func (c *FanoutConsumer) dispatchPublishFanout(ctx context.Context, postID, authorID, createdAt, followers int64) error {
	viaCache, err := c.shouldAppendToCache(ctx, authorID, followers)
	if err != nil {
		return err
	}
	if viaCache {
		return c.appendOrFallback(ctx, postID, authorID, createdAt) // ①② 大V供给
	}
	return c.fanoutToInbox(ctx, postID, authorID, createdAt) // ③ 普通作者
}

// shouldAppendToCache 防抖动判定（①②③）：返回是否经发件箱缓存供给。
// ① 粉丝 ≥ 晋升线：设/刷大V标识（覆盖刷新 TTL）→ 缓存；
// ② 粉丝 < 线但标识在（保持期）→ 缓存（不续 TTL，窗口自然过期后回归③）；
// ③ 粉丝 < 线且无标识 → 扇出。标识读写失败 → RETRY 重投（瞬时错误，不静默降级）
func (c *FanoutConsumer) shouldAppendToCache(ctx context.Context, authorID, followers int64) (bool, error) {
	if followers >= c.fanoutCap {
		if err := c.Timeline.MarkBigV(ctx, authorID); err != nil {
			return false, fmt.Errorf("fanout: mark bigv author %d: %w", authorID, err)
		}
		return true, nil
	}
	marked, err := c.Timeline.IsBigV(ctx, authorID)
	if err != nil {
		return false, fmt.Errorf("fanout: read bigv mark author %d: %w", authorID, err)
	}
	return marked, nil
}

// appendOrFallback 大V供给：写发件箱缓存；失败 fallback 全量扇出（宁重不漏）：
// 新口径下读侧"miss=收件箱路"，帖子必须有收件箱供给线，不允许因缓存故障丢失
func (c *FanoutConsumer) appendOrFallback(ctx context.Context, postID, authorID, createdAt int64) error {
	if err := c.Timeline.Append(ctx, authorID, postID, createdAt); err == nil {
		return nil
	} else {
		c.log.Warn("timeline cache append failed，fallback 扇出",
			"author_id", authorID, "post_id", postID, "error", err)
	}

	fans, err := c.fetchFollowers(ctx, authorID)
	if err != nil {
		return err
	}
	return c.Fanout.WriteInbox(ctx, postID, authorID, createdAt, fans)
}

// fanoutToInbox 普通作者扇出：拉全量粉丝写扩散（幂等 ON CONFLICT DO NOTHING，重投安全）。
// 判定与拉取间粉丝小幅波动不设安全阀（秒级窗口差异个位数，多扇几行可接受，以判定为准）
func (c *FanoutConsumer) fanoutToInbox(ctx context.Context, postID, authorID, createdAt int64) error {
	fans, err := c.fetchFollowers(ctx, authorID)
	if err != nil {
		return err
	}
	return c.Fanout.WriteInbox(ctx, postID, authorID, createdAt, fans)
}

// fetchFollowers 游标分页拉取全量粉丝 uid（GetFollowers，limit 取上限减往返）。
// 契约：列表为空即翻页到底（暂无粉丝同理）
func (c *FanoutConsumer) fetchFollowers(ctx context.Context, authorID int64) ([]int64, error) {
	var fans []int64
	uid := strconv.FormatInt(authorID, 10)
	cursor := &commonpb.Cursor{Limit: defaults.MaxLimit}
	for {
		resp, err := c.UserService.GetFollowers(ctx, connect.NewRequest(&userv1.GetFollowersRequest{
			Uid: uid, Cursor: cursor,
		}))
		if err != nil {
			return nil, fmt.Errorf("user.GetFollowers failed: %w", err)
		}
		if resp.Msg.GetFail() {
			return nil, fmt.Errorf("user.GetFollowers fail: %s", resp.Msg.GetErr().GetMsg())
		}
		briefs := resp.Msg.GetUserBriefs()
		for _, b := range briefs {
			uid, perr := strconv.ParseInt(b.GetUid(), 10, 64)
			if perr != nil {
				c.log.Warn("skip malformed follower uid", "author_id", authorID, "uid", b.GetUid(), "error", perr)
				continue
			}
			fans = append(fans, uid)
		}
		if len(briefs) == 0 {
			return fans, nil
		}
		cursor = resp.Msg.GetCursor()
		if cursor == nil {
			return fans, nil
		}
	}
}

// postIndexFields post 实体 → 索引字段集合（proto → map 提取的薄编排，
// 索引文档组装/类型降级收敛在 meili.NewPostIndexDoc）。
