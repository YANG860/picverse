package consumer

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	commonv1 "rednote/internal/common/v1"
	"rednote/packages/meili"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMeili struct {
	meili.MeiliRepo

	upsertErr error
	deleteErr error

	upserted []meili.PostIndexDoc
	deleted  []string
}

func (f *fakeMeili) HealthCheck(context.Context) error { return nil }

func (f *fakeMeili) EnsurePostsIndex(context.Context) error { return nil }

func (f *fakeMeili) UpsertPost(_ context.Context, doc meili.PostIndexDoc) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserted = append(f.upserted, doc)
	return nil
}

func (f *fakeMeili) RemovePost(_ context.Context, postID string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, postID)
	return nil
}

// fakePostService 覆写 GetPostsByIDs（未覆写方法调用会 panic）
type fakePostService struct {
	postv1connect.PostServiceClient

	resp *postv1.GetPostsByIDsResponse
	err  error
}

func (f *fakePostService) GetPostsByIDs(_ context.Context, _ *connect.Request[postv1.GetPostsByIDsRequest]) (*connect.Response[postv1.GetPostsByIDsResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(f.resp), nil
}

func newTestIndexConsumer(m meili.MeiliRepo, p postv1connect.PostServiceClient, d commonv1.ProcessedMessageRepository) *DiscoverIndexConsumer {
	return &DiscoverIndexConsumer{
		MessageLoop: &MessageLoop[*postv1.PostMessage]{Consumer: Consumer{log: slog.Default()}},
		Meili:       m,
		PostService: p,
		Dedup:       d,
	}
}

// publishedPost 构造 PUBLISHED 帖子实体
func publishedPost(postID string) *postv1.Post {
	return &postv1.Post{
		PostId:    postID,
		Title:     "标题",
		FullText:  "正文内容",
		AuthorId:  "1700000000000000002",
		CreatedAt: "1700000000123",
		PostStat: &postv1.PostStat{
			LikeCount: 1, FavouriteCount: 2, CommentCount: 3, ViewCount: 4,
		},
		Status: postv1.PostStatus_POST_STATUS_PUBLISHED,
	}
}

const testMsgID = "1700000000000000009"

// TestHandleUpsertPostIndex 发布 → 文档组装与字段映射正确、幂等登记 handler 名正确
func TestHandleUpsertPostIndex(t *testing.T) {
	const postID = "1700000000000000001"
	m := &fakeMeili{}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{
		Results: []*postv1.PostResult{{PostId: postID, Post: publishedPost(postID)}},
	}}
	d := &fakeDedup{applied: true}
	c := newTestIndexConsumer(m, p, d)

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: postID}, testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	require.Len(t, m.upserted, 1)
	doc := m.upserted[0]
	assert.Equal(t, postID, doc.PostID)
	assert.Equal(t, "标题", doc.Title)
	assert.Equal(t, "正文内容", doc.FullText)
	assert.Equal(t, "1700000000000000002", doc.AuthorID)
	assert.Equal(t, int64(1700000000123), doc.CreatedAt)
	assert.Equal(t, int64(1), doc.LikeCount)
	assert.Equal(t, int64(2), doc.FavouriteCount)
	assert.Equal(t, int64(3), doc.CommentCount)
	assert.Equal(t, int64(4), doc.ViewCount)

	assert.Equal(t, int64(1700000000000000009), d.gotID)
	assert.Equal(t, "upsert_post_index", d.gotHandler)
}

// TestHandleUpsertPostIndex_CreatedAtDegraded created_at 非法降级为 0 仍索引（不阻塞链路）
func TestHandleUpsertPostIndex_CreatedAtDegraded(t *testing.T) {
	const postID = "1700000000000000001"
	post := publishedPost(postID)
	post.CreatedAt = "not-a-timestamp"
	m := &fakeMeili{}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{
		Results: []*postv1.PostResult{{PostId: postID, Post: post}},
	}}
	c := newTestIndexConsumer(m, p, &fakeDedup{applied: true})

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: postID}, testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	require.Len(t, m.upserted, 1)
	assert.Equal(t, int64(0), m.upserted[0].CreatedAt)
}

// TestHandleUpsertPostIndex_SkipNotPublished 非 PUBLISHED（并发删除/封禁/乱序）不进索引，ACK 丢弃
func TestHandleUpsertPostIndex_SkipNotPublished(t *testing.T) {
	const postID = "1700000000000000001"
	post := publishedPost(postID)
	post.Status = postv1.PostStatus_POST_STATUS_DELETED
	m := &fakeMeili{}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{
		Results: []*postv1.PostResult{{PostId: postID, Post: post}},
	}}
	d := &fakeDedup{applied: true}
	c := newTestIndexConsumer(m, p, d)

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: postID}, testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Empty(t, m.upserted, "非 PUBLISHED 不得写索引")
	assert.Zero(t, d.execCalls, "跳过时不得登记去重")
}

// TestHandleUpsertPostIndex_ResultFail 结果项失败（帖子不存在）降级跳过，ACK
func TestHandleUpsertPostIndex_ResultFail(t *testing.T) {
	const postID = "1700000000000000001"
	m := &fakeMeili{}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{
		Results: []*postv1.PostResult{{PostId: postID, Fail: true}},
	}}
	c := newTestIndexConsumer(m, p, &fakeDedup{applied: true})

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: postID}, testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Empty(t, m.upserted)
}

// TestHandleUpsertPostIndex_Duplicate 重复投递（去重未生效）跳过写索引，ACK
func TestHandleUpsertPostIndex_Duplicate(t *testing.T) {
	const postID = "1700000000000000001"
	m := &fakeMeili{}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{
		Results: []*postv1.PostResult{{PostId: postID, Post: publishedPost(postID)}},
	}}
	d := &fakeDedup{applied: false}
	c := newTestIndexConsumer(m, p, d)

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: postID}, testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Empty(t, m.upserted, "重复消息不得重复写索引")
	assert.Equal(t, int64(1700000000000000009), d.gotID)
}

// TestHandleUpsertPostIndex_RPCError 下游 RPC 失败 → RETRY 重投
func TestHandleUpsertPostIndex_RPCError(t *testing.T) {
	m := &fakeMeili{}
	p := &fakePostService{err: errors.New("rpc down")}
	c := newTestIndexConsumer(m, p, &fakeDedup{applied: true})

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: "1700000000000000001"}, testMsgID)
	require.Error(t, err)
	assert.False(t, ack, "RPC 失败必须 Nak 重投")
	assert.Empty(t, m.upserted)
}

// TestHandleUpsertPostIndex_MeiliError 索引写入失败 → RETRY 重投（去重登记随事务回滚）
func TestHandleUpsertPostIndex_MeiliError(t *testing.T) {
	const postID = "1700000000000000001"
	m := &fakeMeili{upsertErr: errors.New("meili down")}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{
		Results: []*postv1.PostResult{{PostId: postID, Post: publishedPost(postID)}},
	}}
	c := newTestIndexConsumer(m, p, &fakeDedup{applied: true})

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: postID}, testMsgID)
	require.Error(t, err)
	assert.False(t, ack)
}

// TestHandleUpsertPostIndex_InvalidIDs 消息 ID 非法 → RETRY 等正确消息；post ID 非法 → ACK 丢弃
func TestHandleUpsertPostIndex_InvalidIDs(t *testing.T) {
	m := &fakeMeili{}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{}}
	d := &fakeDedup{applied: true}
	c := newTestIndexConsumer(m, p, d)

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: "1700000000000000001"}, "not-a-msg-id")
	require.Error(t, err)
	assert.False(t, ack, "非法消息 ID 必须重投等正确消息")
	assert.Zero(t, d.execCalls, "非法消息 ID 不得登记去重")

	ack, err = c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: "bad-id"}, testMsgID)
	require.Error(t, err)
	assert.True(t, ack, "非法 post ID 不可恢复，ACK 丢弃")
	assert.Empty(t, m.upserted)
}

// TestHandleRemovePostIndex 软删/封禁 → 按主键删除索引文档
func TestHandleRemovePostIndex(t *testing.T) {
	const postID = "1700000000000000001"
	m := &fakeMeili{}
	d := &fakeDedup{applied: true}
	c := newTestIndexConsumer(m, &fakePostService{}, d)

	ack, err := c.handleRemovePostIndex(context.Background(), postID, testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Equal(t, []string{postID}, m.deleted)
	assert.Equal(t, "delete_post_index", d.gotHandler)
}

// TestHandleRemovePostIndex_Duplicate 重复投递跳过删除，ACK
func TestHandleRemovePostIndex_Duplicate(t *testing.T) {
	m := &fakeMeili{}
	c := newTestIndexConsumer(m, &fakePostService{}, &fakeDedup{applied: false})

	ack, err := c.handleRemovePostIndex(context.Background(), "1700000000000000001", testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Empty(t, m.deleted)
}

// TestHandleRemovePostIndex_MeiliError 删除失败 → RETRY 重投
func TestHandleRemovePostIndex_MeiliError(t *testing.T) {
	m := &fakeMeili{deleteErr: errors.New("meili down")}
	c := newTestIndexConsumer(m, &fakePostService{}, &fakeDedup{applied: true})

	ack, err := c.handleRemovePostIndex(context.Background(), "1700000000000000001", testMsgID)
	require.Error(t, err)
	assert.False(t, ack)
}

// TestHandleRemovePostIndex_InvalidIDs 消息 ID 非法 → RETRY；post ID 非法 → ACK 丢弃
func TestHandleRemovePostIndex_InvalidIDs(t *testing.T) {
	m := &fakeMeili{}
	d := &fakeDedup{applied: true}
	c := newTestIndexConsumer(m, &fakePostService{}, d)

	ack, err := c.handleRemovePostIndex(context.Background(), "1700000000000000001", "bad")
	require.Error(t, err)
	assert.False(t, ack)

	ack, err = c.handleRemovePostIndex(context.Background(), "bad-id", testMsgID)
	require.Error(t, err)
	assert.True(t, ack)
	assert.Empty(t, m.deleted)
	assert.Zero(t, d.execCalls)
}

// ==================== 发帖侧推拉分流（handlePublishFanout） ====================

// fakeUserClient 覆写 GetUserProfile / GetFollowers（未覆写方法调用会 panic）。
// pages 为 GetFollowers 分页 uid 列表（每页一批，末页无 cursor）
