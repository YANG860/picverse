package consumer

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	commonpb "rednote/gen/go/common/v1"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	userv1 "rednote/gen/go/user/v1"
	userv1connect "rednote/gen/go/user/v1/userv1connect"
	commonv1 "rednote/internal/common/v1"
	discoverv1 "rednote/internal/discover/v1"
	"rednote/packages/cache"
	"rednote/packages/meili"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMeili 覆写帖子入库领域接口（未覆写方法调用会 panic），记录调用并支持错误注入
// （索引/文档/设置已收敛在 meili 包，此处只桩领域写入/删除）
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

func newTestDiscoverConsumer(m meili.MeiliRepo, p postv1connect.PostServiceClient, d commonv1.ProcessedMessageRepository) *DiscoverConsumer {
	return &DiscoverConsumer{
		Consumer:    Consumer{log: slog.Default()},
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
	c := newTestDiscoverConsumer(m, p, d)

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
	c := newTestDiscoverConsumer(m, p, &fakeDedup{applied: true})

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
	c := newTestDiscoverConsumer(m, p, d)

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
	c := newTestDiscoverConsumer(m, p, &fakeDedup{applied: true})

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
	c := newTestDiscoverConsumer(m, p, d)

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
	c := newTestDiscoverConsumer(m, p, &fakeDedup{applied: true})

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
	c := newTestDiscoverConsumer(m, p, &fakeDedup{applied: true})

	ack, err := c.handleUpsertPostIndex(context.Background(), &postv1.PublishPost{PostId: postID}, testMsgID)
	require.Error(t, err)
	assert.False(t, ack)
}

// TestHandleUpsertPostIndex_InvalidIDs 消息 ID 非法 → RETRY 等正确消息；post ID 非法 → ACK 丢弃
func TestHandleUpsertPostIndex_InvalidIDs(t *testing.T) {
	m := &fakeMeili{}
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{}}
	d := &fakeDedup{applied: true}
	c := newTestDiscoverConsumer(m, p, d)

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
	c := newTestDiscoverConsumer(m, &fakePostService{}, d)

	ack, err := c.handleRemovePostIndex(context.Background(), postID, testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Equal(t, []string{postID}, m.deleted)
	assert.Equal(t, "delete_post_index", d.gotHandler)
}

// TestHandleRemovePostIndex_Duplicate 重复投递跳过删除，ACK
func TestHandleRemovePostIndex_Duplicate(t *testing.T) {
	m := &fakeMeili{}
	c := newTestDiscoverConsumer(m, &fakePostService{}, &fakeDedup{applied: false})

	ack, err := c.handleRemovePostIndex(context.Background(), "1700000000000000001", testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Empty(t, m.deleted)
}

// TestHandleRemovePostIndex_MeiliError 删除失败 → RETRY 重投
func TestHandleRemovePostIndex_MeiliError(t *testing.T) {
	m := &fakeMeili{deleteErr: errors.New("meili down")}
	c := newTestDiscoverConsumer(m, &fakePostService{}, &fakeDedup{applied: true})

	ack, err := c.handleRemovePostIndex(context.Background(), "1700000000000000001", testMsgID)
	require.Error(t, err)
	assert.False(t, ack)
}

// TestHandleRemovePostIndex_InvalidIDs 消息 ID 非法 → RETRY；post ID 非法 → ACK 丢弃
func TestHandleRemovePostIndex_InvalidIDs(t *testing.T) {
	m := &fakeMeili{}
	d := &fakeDedup{applied: true}
	c := newTestDiscoverConsumer(m, &fakePostService{}, d)

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
type fakeUserClient struct {
	userv1connect.UserServiceClient

	followers  int64
	profileErr error
	pages      [][]string
}

func (f *fakeUserClient) GetUserProfile(_ context.Context, _ *connect.Request[userv1.GetUserProfileRequest]) (*connect.Response[userv1.GetUserProfileResponse], error) {
	if f.profileErr != nil {
		return nil, f.profileErr
	}
	return connect.NewResponse(&userv1.GetUserProfileResponse{
		Profile: &userv1.UserProfile{Stat: &userv1.UserStat{Followers: f.followers}},
	}), nil
}

func (f *fakeUserClient) GetFollowers(_ context.Context, _ *connect.Request[userv1.GetFollowersRequest]) (*connect.Response[userv1.GetFollowersResponse], error) {
	if len(f.pages) == 0 {
		return connect.NewResponse(&userv1.GetFollowersResponse{}), nil // 翻页到底
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	var briefs []*userv1.UserBrief
	for _, uid := range page {
		briefs = append(briefs, &userv1.UserBrief{Uid: uid})
	}
	resp := &userv1.GetFollowersResponse{UserBriefs: briefs}
	if len(f.pages) > 0 {
		resp.Cursor = &commonpb.Cursor{Str: "next"}
	}
	return connect.NewResponse(resp), nil
}

// fakeFanout 扇出替身（嵌入接口只覆写 WriteInbox）：记录 WriteInbox 参数
type fakeFanout struct {
	discoverv1.FanoutRepo
	writeErr     error
	gotPostID    int64
	gotAuthorID  int64
	gotCreatedAt int64
	gotFans      []int64
}

func (f *fakeFanout) WriteInbox(_ context.Context, postID, authorID, postCreatedAt int64, followerUIDs []int64) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.gotPostID, f.gotAuthorID, f.gotCreatedAt = postID, authorID, postCreatedAt
	f.gotFans = append([]int64(nil), followerUIDs...)
	return nil
}

// fakeTimeline 发件箱缓存替身（只桩 Append）
type fakeTimeline struct {
	cache.AuthorTimelineCache

	appendErr    error
	appends      int
	gotAuthorID  int64
	gotPostID    int64
	gotCreatedAt int64
}

func (f *fakeTimeline) Append(_ context.Context, authorID, postID, createdAt int64) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.appends++
	f.gotAuthorID, f.gotPostID, f.gotCreatedAt = authorID, postID, createdAt
	return nil
}

const (
	testPostID   = "1700000000000000001"
	testAuthorID = "1700000000000000002"
)

// newTestFanoutConsumer 构造发帖侧测试消费者（fanoutCap=3 便于触发溢出安全阀）
func newTestFanoutConsumer(p postv1connect.PostServiceClient, u userv1connect.UserServiceClient,
	fo discoverv1.FanoutRepo, tl cache.AuthorTimelineCache, d commonv1.ProcessedMessageRepository) *DiscoverConsumer {
	if p == nil {
		p = &fakePostService{resp: &postv1.GetPostsByIDsResponse{
			Results: []*postv1.PostResult{{PostId: testPostID, Post: publishedPost(testPostID)}},
		}}
	}
	return &DiscoverConsumer{
		Consumer:    Consumer{log: slog.Default()},
		PostService: p,
		UserService: u,
		Fanout:      fo,
		Timeline:    tl,
		Dedup:       d,
		fanoutCap:   3,
	}
}

func fanoutMsg() *postv1.PublishPost {
	return &postv1.PublishPost{PostId: testPostID, AuthorId: testAuthorID}
}

// TestHandlePublishFanout_Fanout 普通作者（followers < 晋升线）：全量粉丝扇出、不写缓存
func TestHandlePublishFanout_Fanout(t *testing.T) {
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001", "2002"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	d := &fakeDedup{applied: true}
	c := newTestFanoutConsumer(nil, u, fo, tl, d)

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	assert.Equal(t, int64(1700000000000000001), fo.gotPostID)
	assert.Equal(t, int64(1700000000000000002), fo.gotAuthorID)
	assert.Equal(t, int64(1700000000123), fo.gotCreatedAt)
	assert.Equal(t, []int64{2001, 2002}, fo.gotFans, "全量粉丝扇出")
	assert.Zero(t, tl.appends, "普通作者不写发件箱缓存")

	assert.Equal(t, int64(1700000000000000009), d.gotID)
	assert.Equal(t, "fanout_publish", d.gotHandler)
}

// TestHandlePublishFanout_PullCache 大V（followers ≥ 晋升线）：写发件箱缓存、不扇出不拉粉丝
func TestHandlePublishFanout_PullCache(t *testing.T) {
	u := &fakeUserClient{followers: 100, pages: [][]string{{"2001"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	c := newTestFanoutConsumer(nil, u, fo, tl, &fakeDedup{applied: true})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	assert.Zero(t, fo.gotPostID, "大V不扇出")
	assert.Len(t, u.pages, 1, "大V不拉粉丝列表")
	require.Equal(t, 1, tl.appends)
	assert.Equal(t, int64(1700000000000000002), tl.gotAuthorID)
	assert.Equal(t, int64(1700000000000000001), tl.gotPostID)
	assert.Equal(t, int64(1700000000123), tl.gotCreatedAt)
}

// TestHandlePublishFanout_FollowerOverflow 拉粉丝触达安全阀（判定后粉丝激增 race）：
// 不扇出、转写缓存走 pull（宁重不漏）
func TestHandlePublishFanout_FollowerOverflow(t *testing.T) {
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001", "2002", "2003"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	c := newTestFanoutConsumer(nil, u, fo, tl, &fakeDedup{applied: true})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	assert.Zero(t, fo.gotPostID, "溢出转 pull，不扇出")
	assert.Equal(t, 1, tl.appends)
}

// TestHandlePublishFanout_AppendFallback 大V写缓存失败：fallback 全量扇出（宁重不漏，不允许无供给线）
func TestHandlePublishFanout_AppendFallback(t *testing.T) {
	u := &fakeUserClient{followers: 100, pages: [][]string{{"2001"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{appendErr: errors.New("redis down")}
	c := newTestFanoutConsumer(nil, u, fo, tl, &fakeDedup{applied: true})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	assert.Equal(t, []int64{2001}, fo.gotFans, "缓存写失败降级扇出，保证收件箱供给线")
}

// TestHandlePublishFanout_DedupSkip 重复投递：去重跳过，无副作用
func TestHandlePublishFanout_DedupSkip(t *testing.T) {
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	c := newTestFanoutConsumer(nil, u, fo, tl, &fakeDedup{applied: false})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	assert.Zero(t, fo.gotPostID)
	assert.Zero(t, tl.appends)
}

// TestHandlePublishFanout_ProfileError 取粉丝数失败：RETRY 重投
func TestHandlePublishFanout_ProfileError(t *testing.T) {
	u := &fakeUserClient{profileErr: errors.New("boom")}
	c := newTestFanoutConsumer(nil, u, &fakeFanout{}, &fakeTimeline{}, &fakeDedup{applied: true})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.ErrorContains(t, err, "GetUserProfile")
	assert.False(t, ack, "可重试错误 RETRY 重投")
}

// TestHandlePublishFanout_CreatedAtDegraded created_at 非法降级 0 继续扇出（不阻塞链路）
func TestHandlePublishFanout_CreatedAtDegraded(t *testing.T) {
	post := publishedPost(testPostID)
	post.CreatedAt = "not-a-timestamp"
	p := &fakePostService{resp: &postv1.GetPostsByIDsResponse{
		Results: []*postv1.PostResult{{PostId: testPostID, Post: post}},
	}}
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001"}}}
	fo := &fakeFanout{}
	c := newTestFanoutConsumer(p, u, fo, &fakeTimeline{}, &fakeDedup{applied: true})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Equal(t, int64(0), fo.gotCreatedAt, "created_at 非法降级 0")
}
