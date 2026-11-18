package consumer

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"
	postv1 "rednote/gen/go/post/v1"
	media "rednote/internal/media/models"
	models "rednote/internal/post/models"
	v1 "rednote/internal/post/v1"
	"rednote/packages/defaults"
	"rednote/packages/mq"
	"rednote/test/testmq"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// fakePostRepo 覆写 handleDeletePost 所需方法（未覆写方法调用会 panic）；
// DeletePostMedias 返回一次被删关联后置空，模拟重复投递时 0 行命中（幂等守卫）
type fakePostRepo struct {
	v1.PostRepository

	deletePostErr     error
	deleteMediasErr   error
	deleteCommentsErr error
	deleteLikesErr    error
	deleteFavsErr     error
	assetIDs          []int64

	gotDeletePost     bool
	gotDeleteMedias   bool
	gotDeleteComments bool
	gotDeleteLikes    bool
	gotDeleteFavs     bool
	deleteCommentsN   int
	commentsPostID    int64
	likesN            int
	favsN             int

	// handleAuditPost 所需：媒体关联查询与状态流转记录
	medias          []*models.PostMedia
	mediasErr       error
	setStatusErr    error
	setStatusPostID int64
	setStatusFrom   postv1.PostStatus
	setStatusTo     postv1.PostStatus
}

func (f *fakePostRepo) Transaction(fn func(repo v1.PostRepository) error) error {
	return fn(f)
}

func (f *fakePostRepo) DB() *gorm.DB { return nil }

func (f *fakePostRepo) DeletePost(_ context.Context, _, _ int64) (gen.ResultInfo, error) {
	f.gotDeletePost = true
	if f.deletePostErr != nil {
		return gen.ResultInfo{}, f.deletePostErr
	}
	return gen.ResultInfo{}, nil
}

func (f *fakePostRepo) DeletePostMedias(_ context.Context, _ int64) ([]int64, error) {
	f.gotDeleteMedias = true
	if f.deleteMediasErr != nil {
		return nil, f.deleteMediasErr
	}
	ids := f.assetIDs
	f.assetIDs = nil
	return ids, nil
}

func (f *fakePostRepo) DeletePostComments(_ context.Context, postID int64) (gen.ResultInfo, error) {
	f.gotDeleteComments = true
	f.deleteCommentsN++
	f.commentsPostID = postID
	if f.deleteCommentsErr != nil {
		return gen.ResultInfo{}, f.deleteCommentsErr
	}
	return gen.ResultInfo{}, nil
}

func (f *fakePostRepo) DeletePostLikes(_ context.Context, _ int64) (gen.ResultInfo, error) {
	f.gotDeleteLikes = true
	f.likesN++
	if f.deleteLikesErr != nil {
		return gen.ResultInfo{}, f.deleteLikesErr
	}
	return gen.ResultInfo{}, nil
}

func (f *fakePostRepo) DeletePostFavourites(_ context.Context, _ int64) (gen.ResultInfo, error) {
	f.gotDeleteFavs = true
	f.favsN++
	if f.deleteFavsErr != nil {
		return gen.ResultInfo{}, f.deleteFavsErr
	}
	return gen.ResultInfo{}, nil
}

func (f *fakePostRepo) GetPostMedias(_ context.Context, _ []int64) ([]*models.PostMedia, error) {
	if f.mediasErr != nil {
		return nil, f.mediasErr
	}
	return f.medias, nil
}

func (f *fakePostRepo) SetPostStatus(_ context.Context, postID int64, from, to postv1.PostStatus) (gen.ResultInfo, error) {
	f.setStatusPostID = postID
	f.setStatusFrom = from
	f.setStatusTo = to
	if f.setStatusErr != nil {
		return gen.ResultInfo{}, f.setStatusErr
	}
	return gen.ResultInfo{RowsAffected: 1}, nil
}

// derefAssetIDs 解出 media 流上的 DerefMedia 消息资产 ID 序列
func derefAssetIDs(t *testing.T, c *testmq.Client) []string {
	t.Helper()
	var ids []string
	for _, m := range c.SentBySubject(defaults.MediaMessageStream) {
		var msg mediav1.MediaMessage
		require.NoError(t, proto.Unmarshal(m.PayLoad, &msg))
		require.NotNil(t, msg.GetDerefMedia(), "删帖回收必须发 DerefMedia 卸载消息")
		ids = append(ids, msg.GetDerefMedia().GetAssetId())
	}
	return ids
}

func newDeletePostConsumer(repo v1.PostRepository, mqClient mq.MQClient) *PostConsumer {
	return &PostConsumer{
		MessageLoop: &MessageLoop[*postv1.PostMessage]{Consumer: Consumer{MQ: mqClient, log: slog.Default(), maxRetry: testPostMaxRetry}},
		Repo:        repo,
	}
}

// TestHandleDeletePost 删帖回收：物理删除 + 按被删关联逐槽位写 outbox 卸载消息（计数回收必达不漏）
func TestHandleDeletePost(t *testing.T) {
	msg := &postv1.DeletePost{PostId: "9001", AuthorId: "8001"}

	t.Run("删除并发卸载消息：删几槽发几条", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001, 6002}}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.True(t, repo.gotDeletePost)
		assert.True(t, repo.gotDeleteMedias)
		assert.True(t, repo.gotDeleteComments, "删帖连带物理回收评论")
		assert.True(t, repo.gotDeleteLikes, "删帖连带物理回收点赞明细")
		assert.True(t, repo.gotDeleteFavs, "删帖连带物理回收收藏明细")
		assert.Equal(t, int64(9001), repo.commentsPostID)
		assert.Equal(t, []string{"6001", "6002"}, derefAssetIDs(t, mqc))

		sent := mqc.SentBySubject(defaults.MediaMessageStream)
		require.Len(t, sent, 2)
		assert.NotEqual(t, sent[0].ID, sent[1].ID, "每条消息唯一 ID，媒体侧幂等去重")
	})

	t.Run("重复投递幂等：无被删关联不再发消息", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		_, err := c.handleDeletePost(context.Background(), msg)
		require.NoError(t, err)
		require.Len(t, mqc.SentBySubject(defaults.MediaMessageStream), 1)

		ack, err := c.handleDeletePost(context.Background(), msg)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Len(t, mqc.SentBySubject(defaults.MediaMessageStream), 1, "第二次不重复发卸载消息")
		assert.Equal(t, 2, repo.deleteCommentsN, "每次投递都执行评论回收（DB 侧 0 行命中幂等）")
		assert.Equal(t, 2, repo.likesN, "点赞明细回收同样幂等（0 行命中）")
		assert.Equal(t, 2, repo.favsN, "收藏明细回收同样幂等（0 行命中）")
	})

	t.Run("物理删除失败 RETRY 不发消息", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}, deletePostErr: errors.New("db down")}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("关联删除失败 RETRY 不发消息", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}, deleteMediasErr: errors.New("db down")}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("评论回收失败 RETRY 不发消息（同事务回滚，不漏清）", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}, deleteCommentsErr: errors.New("db down")}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("点赞明细回收失败 RETRY 不发消息（同事务回滚）", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}, deleteLikesErr: errors.New("db down")}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("收藏明细回收失败 RETRY 不发消息（同事务回滚）", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}, deleteFavsErr: errors.New("db down")}
		mqc := testmq.New()
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("outbox 写入失败 RETRY（事务回滚，不漏减）", func(t *testing.T) {
		repo := &fakePostRepo{assetIDs: []int64{6001}}
		mqc := testmq.New()
		mqc.SetSendError(errors.New("outbox write fail"))
		c := newDeletePostConsumer(repo, mqc)

		ack, err := c.handleDeletePost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
	})
}

// fakeAuditMediaClient 覆写 GetMediaAssets（未覆写方法调用会 panic）；
// 支持逐项结果（batch）与连接层错误（rpcErr）注入，记录请求资产 id
type fakeAuditMediaClient struct {
	mediav1connect.MediaServiceClient

	batch  []*mediav1.MediaAssetResult
	rpcErr error

	gotIDs []string
}

func (f *fakeAuditMediaClient) GetMediaAssets(_ context.Context, req *connect.Request[mediav1.GetMediaAssetsRequest]) (*connect.Response[mediav1.GetMediaAssetsResponse], error) {
	f.gotIDs = req.Msg.GetIds()
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(&mediav1.GetMediaAssetsResponse{Results: f.batch}), nil
}

// assetResult 构造单资产就绪性核验结果（id 回填为服务端规范化后的十进制串）
func assetResult(id int64, status mediav1.MediaAssetStatus) *mediav1.MediaAssetResult {
	return &mediav1.MediaAssetResult{
		Id:    strconv.FormatInt(id, 10),
		Asset: &mediav1.MediaAsset{Status: status},
	}
}

// publishPosts 解出 post 流上的 PublishPost 发布消息序列
func publishPosts(t *testing.T, c *testmq.Client) []*postv1.PublishPost {
	t.Helper()
	var out []*postv1.PublishPost
	for _, m := range c.SentBySubject(defaults.PostMessageStream) {
		var msg postv1.PostMessage
		require.NoError(t, proto.Unmarshal(m.PayLoad, &msg))
		require.NotNil(t, msg.GetPublishPost(), "必须发 PublishPost 发布消息")
		out = append(out, msg.GetPublishPost())
	}
	return out
}

// testPostMaxRetry 测试基座的投递上限（与 config.consumer.post.max_retry 同值；
// handler 的重试耗尽判定依赖它，置 0 会让“窗口耗尽”分支恒真）
const testPostMaxRetry = 8

func newAuditPostConsumer(repo v1.PostRepository, mqClient mq.MQClient, mediaSvc mediav1connect.MediaServiceClient) *PostConsumer {
	return newAuditPostConsumerWithLog(repo, mqClient, mediaSvc, slog.Default())
}

func newAuditPostConsumerWithLog(repo v1.PostRepository, mqClient mq.MQClient, mediaSvc mediav1connect.MediaServiceClient, log *slog.Logger) *PostConsumer {
	return &PostConsumer{
		MessageLoop:  &MessageLoop[*postv1.PostMessage]{Consumer: Consumer{MQ: mqClient, log: log, maxRetry: testPostMaxRetry}},
		Repo:         repo,
		MediaService: mediaSvc,
	}
}

// captureHandler 收集日志记录（供断言“业务裁决只记 Warn 且留痕 reason，不返回 error”）
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

// recordsAt 返回指定级别的日志记录
func (h *captureHandler) recordsAt(level slog.Level) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, 0, len(h.records))
	for _, r := range h.records {
		if r.Level == level {
			out = append(out, r)
		}
	}
	return out
}

// attrString 取记录里的字符串 kv（不存在返回空串）
func attrString(r slog.Record, key string) string {
	var out string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out = a.Value.String()
			return false
		}
		return true
	})
	return out
}

// TestHandleAuditPost 发布前置校验：帖内媒体全部 ACTIVE 才流转 PUBLISHED 并发发布消息；
// 在途瞬时态 RETRY 重投等就绪，终态 ACK 丢弃并回退 DRAFT 供作者修复后重新发布
func TestHandleAuditPost(t *testing.T) {
	msg := &postv1.AuditPost{PostId: "9001", AuthorId: "8001"}

	// mediasOf 按槽位构造媒体关联（slot → asset）
	mediasOf := func(assetIDs ...int64) []*models.PostMedia {
		out := make([]*models.PostMedia, 0, len(assetIDs))
		for _, id := range assetIDs {
			out = append(out, &models.PostMedia{Media: &media.MediaFile{AssetID: id}})
		}
		return out
	}
	// activeResults 构造全部 ACTIVE 的核验结果
	activeResults := func(assetIDs ...int64) []*mediav1.MediaAssetResult {
		out := make([]*mediav1.MediaAssetResult, 0, len(assetIDs))
		for _, id := range assetIDs {
			out = append(out, assetResult(id, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE))
		}
		return out
	}

	t.Run("全部 ACTIVE：流转 PUBLISHED 并发发布消息（同资产多槽位去重核验）", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001, 6001, 6002)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: activeResults(6001, 6002)}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Equal(t, []string{"6001", "6002"}, mc.gotIDs, "同资产多槽位只核验一次")
		assert.Equal(t, int64(9001), repo.setStatusPostID)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_AUDITING, repo.setStatusFrom)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, repo.setStatusTo)

		pubs := publishPosts(t, mqc)
		require.Len(t, pubs, 1)
		assert.Equal(t, "9001", pubs[0].GetPostId())
		assert.Equal(t, "8001", pubs[0].GetAuthorId())
	})

	t.Run("无媒体关联：直接放行不调媒体服务", func(t *testing.T) {
		repo := &fakePostRepo{}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		require.NoError(t, err)
		assert.True(t, ack)
		assert.Empty(t, mc.gotIDs, "无关联不应调媒体服务")
		require.Len(t, publishPosts(t, mqc), 1)
	})

	t.Run("媒体在途（PROCESSING）：RETRY 重投不发布", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: []*mediav1.MediaAssetResult{
			assetResult(6001, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING),
		}}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.False(t, ack, "RETRY 等媒体就绪后重投")
		assert.ErrorIs(t, err, errMediaNotReady)
		assert.Zero(t, repo.setStatusPostID, "未就绪不流转状态")
		assert.Empty(t, mqc.Sent())
	})

	t.Run("媒体在途且投递次数未达上限：继续 RETRY", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: []*mediav1.MediaAssetResult{
			assetResult(6001, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING),
		}}
		c := newAuditPostConsumer(repo, mqc, mc)

		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: testPostMaxRetry - 1})
		ack, err := c.handleAuditPost(ctx, msg)
		assert.False(t, ack, "窗口内仍 RETRY")
		assert.ErrorIs(t, err, errMediaNotReady)
		assert.Zero(t, repo.setStatusPostID, "未耗尽不流转状态")
		assert.Empty(t, mqc.Sent())
	})

	t.Run("媒体在途且重投窗口耗尽：ACK 回退 DRAFT（不留僵死 AUDITING）", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: []*mediav1.MediaAssetResult{
			assetResult(6001, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_PROCESSING),
		}}
		c := newAuditPostConsumer(repo, mqc, mc)

		// 第 max_retry 次投递再 RETRY 就会被 JetStream 丢弃（无死信）→ 帖子永久卡 AUDITING
		ctx := withDelivered(context.Background(), &jetstream.MsgMetadata{NumDelivered: testPostMaxRetry})
		ack, err := c.handleAuditPost(ctx, msg)
		assert.True(t, ack, "耗尽后不得再 NAK（否则消息被静默丢弃）")
		assert.NoError(t, err, "业务裁决（回退草稿）不是处理失败，不得向消息循环返回 error")
		assert.Equal(t, int64(9001), repo.setStatusPostID)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_AUDITING, repo.setStatusFrom)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, repo.setStatusTo, "回退 DRAFT 供作者重新发布")
		assert.Empty(t, mqc.Sent(), "未就绪不发布")
	})

	t.Run("未知媒体状态（未来新增在途态）：按未就绪 RETRY，不误判终态", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: []*mediav1.MediaAssetResult{
			assetResult(6001, mediav1.MediaAssetStatus(99)), // 枚举外的值：模拟 media 侧新增在途态
		}}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.False(t, ack, "未列举的状态保守按未就绪重试")
		assert.ErrorIs(t, err, errMediaNotReady, "不得落入终态分支（否则会被直接回退草稿）")
		assert.Zero(t, repo.setStatusPostID, "不误判为不可用")
		assert.Empty(t, mqc.Sent())
	})

	t.Run("媒体终态（FAILED）：ACK 回退 DRAFT 不发布", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: []*mediav1.MediaAssetResult{
			assetResult(6001, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED),
		}}
		cap := &captureHandler{}
		c := newAuditPostConsumerWithLog(repo, mqc, mc, slog.New(cap))

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.True(t, ack, "终态永不就绪，丢弃不重投")
		assert.NoError(t, err, "业务裁决不是处理失败（reason 只进 Warn 日志）")

		// 归因留痕：一条 Warn、带 post_id 与 reason，且不得记 Error（否则会污染告警）
		warns := cap.recordsAt(slog.LevelWarn)
		require.Len(t, warns, 1, "回退须恰好一条 Warn 留痕")
		assert.Equal(t, "post publish rejected, revert to draft", warns[0].Message)
		assert.Equal(t, "9001", attrString(warns[0], "post_id"))
		assert.Contains(t, attrString(warns[0], "reason"), "media assets unusable")
		assert.Empty(t, cap.recordsAt(slog.LevelError), "预期业务裁决不得记 Error")
		assert.Equal(t, int64(9001), repo.setStatusPostID)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_AUDITING, repo.setStatusFrom)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, repo.setStatusTo, "回退 DRAFT 供作者修复后重新发布")
		assert.Empty(t, mqc.Sent())
	})

	t.Run("资产查无（逐项 fail）：ACK 回退 DRAFT 不发布", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: []*mediav1.MediaAssetResult{{Id: "6001", Fail: true}}}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.True(t, ack, "资产查无是终态，重投只会得到同样裁决")
		assert.NoError(t, err, "业务裁决不是处理失败（reason 只进 Warn 日志）")
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, repo.setStatusTo, "回退 DRAFT")
		assert.Empty(t, mqc.Sent())
	})

	t.Run("槽位数据残缺：ACK 回退 DRAFT 不发布", func(t *testing.T) {
		repo := &fakePostRepo{medias: []*models.PostMedia{{}}} // Media 缺失
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.True(t, ack, "无法核验且不会自行恢复，终态丢弃")
		assert.NoError(t, err, "业务裁决不是处理失败（reason 只进 Warn 日志）")
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, repo.setStatusTo, "回退 DRAFT")
		assert.Empty(t, mqc.Sent())
	})

	t.Run("回退 DRAFT 失败：RETRY 不丢状态", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001), setStatusErr: errors.New("db down")}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: []*mediav1.MediaAssetResult{
			assetResult(6001, mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED),
		}}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.False(t, ack, "RETRY 重投直到回退成功")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("媒体服务连接层瞬时错误：RETRY 不发布", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{rpcErr: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("结果缺项：RETRY 等正确结果", func(t *testing.T) {
		repo := &fakePostRepo{medias: mediasOf(6001, 6002)}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{batch: activeResults(6001)} // 缺 6002
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("查询媒体关联失败 RETRY 不发布", func(t *testing.T) {
		repo := &fakePostRepo{mediasErr: errors.New("db down")}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), msg)
		assert.False(t, ack, "RETRY")
		assert.Error(t, err)
		assert.Empty(t, mqc.Sent())
	})

	t.Run("非法 post_id：ACK 丢弃不处理", func(t *testing.T) {
		repo := &fakePostRepo{}
		mqc := testmq.New()
		mc := &fakeAuditMediaClient{}
		c := newAuditPostConsumer(repo, mqc, mc)

		ack, err := c.handleAuditPost(context.Background(), &postv1.AuditPost{PostId: "not-a-snowflake", AuthorId: "8001"})
		assert.True(t, ack, "业务 ID 非法不可恢复，丢弃不重投")
		assert.Error(t, err)
		assert.Zero(t, repo.setStatusPostID)
		assert.Empty(t, mc.gotIDs, "不查媒体服务")
		assert.Empty(t, mqc.Sent())
	})
}
