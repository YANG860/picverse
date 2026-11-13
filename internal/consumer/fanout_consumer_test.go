package consumer

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	commonpb "rednote/gen/go/common/v1"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	userv1 "rednote/gen/go/user/v1"
	userv1connect "rednote/gen/go/user/v1/userv1connect"
	commonv1 "rednote/internal/common/v1"
	discoverv1 "rednote/internal/discover/v1"
	"rednote/packages/cache"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// fakeTimeline 发件箱缓存替身（只桩 Append/MarkBigV/IsBigV）
type fakeTimeline struct {
	cache.AuthorTimelineCache

	appendErr    error
	appends      int
	gotAuthorID  int64
	gotPostID    int64
	gotCreatedAt int64

	marked    bool // 大V标识状态（防抖动判定）
	markErr   error
	markCnt   int // MarkBigV 调用次数（断言“不刷新 TTL”）
	existsErr error
}

func (f *fakeTimeline) Append(_ context.Context, authorID, postID, createdAt int64) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.appends++
	f.gotAuthorID, f.gotPostID, f.gotCreatedAt = authorID, postID, createdAt
	return nil
}

func (f *fakeTimeline) MarkBigV(context.Context, int64) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.marked = true
	f.markCnt++
	return nil
}

func (f *fakeTimeline) IsBigV(context.Context, int64) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.marked, nil
}

const (
	testPostID   = "1700000000000000001"
	testAuthorID = "1700000000000000002"
)

// newTestFanoutConsumer 构造发帖侧测试消费者（fanoutCap=3 便于触发溢出安全阀）
func newTestFanoutConsumer(p postv1connect.PostServiceClient, u userv1connect.UserServiceClient,
	fo discoverv1.FanoutRepo, tl cache.AuthorTimelineCache, d commonv1.ProcessedMessageRepository) *FanoutConsumer {
	if p == nil {
		p = &fakePostService{resp: &postv1.GetPostsByIDsResponse{
			Results: []*postv1.PostResult{{PostId: testPostID, Post: publishedPost(testPostID)}},
		}}
	}
	return &FanoutConsumer{
		MessageLoop: &MessageLoop[*postv1.PostMessage]{Consumer: Consumer{log: slog.Default()}},
		PostService: p,
		UserService: u,
		Fanout:      fo,
		Timeline:    tl,
		HotPool:     cache.NewHotPoolCache(testredis.New(), nil), // 发帖钩子真身（默认；入池语义用例另建）
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

// TestHandlePublishFanout_NoOverflowValve 判定与拉取间粉丝小幅波动不设安全阀：
// 判定 followers < 线即以判定为准照常扇出（秒级窗口差异个位数，多扇几行可接受）
func TestHandlePublishFanout_NoOverflowValve(t *testing.T) {
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001", "2002", "2003"}}} // 拉到 3 条 ≥ cap
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	c := newTestFanoutConsumer(nil, u, fo, tl, &fakeDedup{applied: true})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	assert.Equal(t, []int64{2001, 2002, 2003}, fo.gotFans, "以判定为准：全量照常扇出")
	assert.Zero(t, tl.appends, "不转 pull")
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

// TestHandlePublishFanout_DebounceMarked 防抖动②：粉丝跌破线但标识在（保持期）——
// 仍写缓存保供给连续，不刷新 TTL（markCnt 不变），不扇出
func TestHandlePublishFanout_DebounceMarked(t *testing.T) {
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{marked: true, markCnt: 1} // 保持期内（此前晋升过）
	d := &fakeDedup{applied: true}
	c := newTestFanoutConsumer(nil, u, fo, tl, d)

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)

	assert.Equal(t, 1, tl.appends, "标识在：仍写发件箱缓存")
	assert.Zero(t, fo.gotPostID, "不扇出收件箱")
	assert.Equal(t, 1, tl.markCnt, "不刷新 TTL（MarkBigV 不调用）")
}

// TestHandlePublishFanout_DebounceSequence 防抖动时序：晋升设标识 → 跌破线仍缓存（TTL 不续）→
// 标识过期回归扇出；再次晋升重设标识
func TestHandlePublishFanout_DebounceSequence(t *testing.T) {
	u := &fakeUserClient{followers: 5, pages: [][]string{{"2001"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	d := &fakeDedup{applied: true}
	c := newTestFanoutConsumer(nil, u, fo, tl, d)
	ctx := context.Background()

	// ① 晋升：≥ 线 → 设标识 + 写缓存
	ack, err := c.handlePublishFanout(ctx, fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.True(t, tl.marked, "晋升设标识")
	assert.Equal(t, 1, tl.markCnt)
	assert.Equal(t, 1, tl.appends)

	// ② 跌破线、标识在（保持期）：仍缓存、不续 TTL、不扇出
	u.followers = 2
	ack, err = c.handlePublishFanout(ctx, fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Equal(t, 2, tl.appends, "保持期：仍写缓存")
	assert.Equal(t, 1, tl.markCnt, "保持期：TTL 不续")
	assert.Zero(t, fo.gotPostID)

	// 标识过期（模拟 mark_ttl 到期）
	tl.marked = false

	// ③ 跌破线且标识过期：回归扇出
	ack, err = c.handlePublishFanout(ctx, fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Equal(t, int64(1700000000000000001), fo.gotPostID, "标识过期：回归 fanout 扇出")
	assert.Equal(t, 2, tl.appends)

	// ④ 再次晋升：重设标识 + 写缓存
	u.followers = 5
	ack, err = c.handlePublishFanout(ctx, fanoutMsg(), testMsgID)
	require.NoError(t, err)
	assert.True(t, ack)
	assert.Equal(t, 2, tl.markCnt, "再次晋升重设标识（覆盖刷新）")
	assert.Equal(t, 3, tl.appends)
}

// TestHandlePublishFanout_MarkError 标识读写失败 → RETRY 重投（瞬时错误语义，不静默降级）
func TestHandlePublishFanout_MarkError(t *testing.T) {
	ctx := context.Background()

	// Mark 失败（晋升路）
	tl := &fakeTimeline{markErr: errors.New("redis down")}
	c := newTestFanoutConsumer(nil, &fakeUserClient{followers: 5}, &fakeFanout{}, tl, &fakeDedup{applied: true})
	ack, err := c.handlePublishFanout(ctx, fanoutMsg(), testMsgID)
	require.Error(t, err)
	assert.False(t, ack)

	// IsBigV 失败（普通判定路）
	tl2 := &fakeTimeline{existsErr: errors.New("redis down")}
	c2 := newTestFanoutConsumer(nil, &fakeUserClient{followers: 2}, &fakeFanout{}, tl2, &fakeDedup{applied: true})
	ack, err = c2.handlePublishFanout(ctx, fanoutMsg(), testMsgID)
	require.Error(t, err)
	assert.False(t, ack)
}

// TestHandlePublishFanout_HotPoolAdd 发帖钩子：发布分流后 0 分占位入热池（新帖秒级可被探索位采样）
func TestHandlePublishFanout_HotPoolAdd(t *testing.T) {
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001", "2002"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	c := newTestFanoutConsumer(nil, u, fo, tl, &fakeDedup{applied: true})

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err)
	require.True(t, ack)

	ms, err := c.HotPool.TopN(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	assert.Equal(t, int64(1700000000000000001), ms[0].PostID)
	assert.Equal(t, int64(1700000000000000002), ms[0].AuthorID, "author 随 member 携带（打散用）")
	assert.Zero(t, ms[0].Score, "0 分占位")
}

// failingZAddNX ZAddNX 失败替身（嵌接口，未覆写方法 panic 兜底）
type failingZAddNX struct{ cache.Cache }

func (failingZAddNX) ZAddNX(context.Context, string, []cache.ZMember, time.Duration) error {
	return errors.New("redis down")
}

// TestHandlePublishFanout_HotPoolAddFail 钩子失败降级：入池是补给非红线，不阻塞发布主链路
func TestHandlePublishFanout_HotPoolAddFail(t *testing.T) {
	u := &fakeUserClient{followers: 2, pages: [][]string{{"2001", "2002"}}}
	fo := &fakeFanout{}
	tl := &fakeTimeline{}
	c := newTestFanoutConsumer(nil, u, fo, tl, &fakeDedup{applied: true})
	c.HotPool = cache.NewHotPoolCache(failingZAddNX{}, nil)

	ack, err := c.handlePublishFanout(context.Background(), fanoutMsg(), testMsgID)
	require.NoError(t, err, "入池失败不阻塞发布（扇出已成功）")
	assert.True(t, ack)
	assert.Equal(t, []int64{2001, 2002}, fo.gotFans, "扇出照常完成")
}
