package v1

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	discoverv1connect "rednote/gen/go/discover/v1/discoverv1connect"
	postv1 "rednote/gen/go/post/v1"
	userv1 "rednote/gen/go/user/v1"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFeedDiscoverClient 扩展 discover 被测路径（三条信息流）；
// 独立类型以保持 fakeDiscoverClient 不实现这些方法的既有回归守卫
type fakeFeedDiscoverClient struct {
	discoverv1connect.DiscoverServiceClient

	followingResp *discoverv1.GetFollowingFeedResponse
	hotResp       *discoverv1.GetHotPostsResponse
	recoResp      *discoverv1.GetRecommendFeedResponse
	rpcErr        error

	gotFollowingReq *discoverv1.GetFollowingFeedRequest
	gotHotReq       *discoverv1.GetHotPostsRequest
	gotRecoReq      *discoverv1.GetRecommendFeedRequest
}

func (f *fakeFeedDiscoverClient) GetFollowingFeed(_ context.Context, req *connect.Request[discoverv1.GetFollowingFeedRequest]) (*connect.Response[discoverv1.GetFollowingFeedResponse], error) {
	f.gotFollowingReq = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(f.followingResp), nil
}

func (f *fakeFeedDiscoverClient) GetHotPosts(_ context.Context, req *connect.Request[discoverv1.GetHotPostsRequest]) (*connect.Response[discoverv1.GetHotPostsResponse], error) {
	f.gotHotReq = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(f.hotResp), nil
}

func (f *fakeFeedDiscoverClient) GetRecommendFeed(_ context.Context, req *connect.Request[discoverv1.GetRecommendFeedRequest]) (*connect.Response[discoverv1.GetRecommendFeedResponse], error) {
	f.gotRecoReq = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(f.recoResp), nil
}

func newBFFForFeedTest(user *fakeUserClient, post *fakeSearchPostClient, discover *fakeFeedDiscoverClient) *BFFUsecaseImpl {
	return &BFFUsecaseImpl{
		Repo: &DownstreamRepo{User: user, Media: &fakeMediaClient{}, Post: post, Discover: discover},
		MQ:   &fakeMQClient{},
		log:  slog.Default(),
	}
}

// TestBFFGetHotPosts 热榜聚合：游标透传 + 组装卡片 + 删帖过滤兜底
func TestBFFGetHotPosts(t *testing.T) {
	disc := &fakeFeedDiscoverClient{hotResp: &discoverv1.GetHotPostsResponse{
		PostIds: []string{"p1", "p2"},
		Cursor:  &commonv1.Cursor{Str: "2", Limit: 10},
	}}
	// p2 已软删（非 PUBLISHED）→ 回查过滤兜底
	post := &fakeSearchPostClient{viewResults: []*postv1.PostBriefResult{
		visibleBrief("p1", "u1", "第一"),
		invisibleBrief("p2"),
	}}
	user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
		{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "a"}},
	}}
	u := newBFFForFeedTest(user, post, disc)

	resp, err := u.GetHotPosts(ctxWithUID("u1"), &bffv1.GetHotPostsRequest{Cursor: &commonv1.Cursor{Str: "0", Limit: 10}})
	require.NoError(t, err)
	require.False(t, resp.GetFail())

	require.NotNil(t, disc.gotHotReq)
	assert.Equal(t, "0", disc.gotHotReq.GetCursor().GetStr(), "游标透传")
	require.Len(t, resp.GetPosts(), 1, "删帖过滤兜底")
	assert.Equal(t, "p1", resp.GetPosts()[0].GetPostId())
	assert.Equal(t, "2", resp.GetCursor().GetStr(), "游标原样透传（过滤不吞页）")
}

// TestBFFGetRecommendFeed 推荐流聚合：uid 由会话注入（seen 归属）+ 组装
func TestBFFGetRecommendFeed(t *testing.T) {
	disc := &fakeFeedDiscoverClient{recoResp: &discoverv1.GetRecommendFeedResponse{PostIds: []string{"p1"}}}
	post := &fakeSearchPostClient{viewResults: []*postv1.PostBriefResult{
		visibleBrief("p1", "u9", "推荐"),
	}}
	user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
		{Uid: "u9", Brief: &userv1.UserBrief{Uid: "u9", Name: "作者"}},
	}}
	u := newBFFForFeedTest(user, post, disc)

	resp, err := u.GetRecommendFeed(ctxWithUID("u1"), &bffv1.GetRecommendFeedRequest{})
	require.NoError(t, err)
	require.False(t, resp.GetFail())

	require.NotNil(t, disc.gotRecoReq)
	assert.Equal(t, "u1", disc.gotRecoReq.GetUid(), "uid 会话注入（seen 账本归属）")
	require.Len(t, resp.GetPosts(), 1)
	assert.Equal(t, "推荐", resp.GetPosts()[0].GetTitle())
	assert.Equal(t, "作者", resp.GetPosts()[0].GetAuthor().GetName())
}

// TestBFFGetFollowingFeed 关注流聚合：拉全量关注（封顶）注入 discover + 组装
func TestBFFGetFollowingFeed(t *testing.T) {
	disc := &fakeFeedDiscoverClient{followingResp: &discoverv1.GetFollowingFeedResponse{
		PostIds: []string{"p1"}, Cursor: &commonv1.Cursor{Str: "5", Limit: 10},
	}}
	post := &fakeSearchPostClient{viewResults: []*postv1.PostBriefResult{
		visibleBrief("p1", "u2", "关注流"),
	}}
	user := &fakeUserClient{followingsResp: &userv1.GetFollowingsResponse{
		UserBriefs: []*userv1.UserBrief{{Uid: "u2"}, {Uid: "u3"}},
	}}
	u := newBFFForFeedTest(user, post, disc)

	resp, err := u.GetFollowingFeed(ctxWithUID("u1"), &bffv1.GetFollowingFeedRequest{Cursor: &commonv1.Cursor{Str: "0", Limit: 10}})
	require.NoError(t, err)
	require.False(t, resp.GetFail())

	require.NotNil(t, disc.gotFollowingReq)
	assert.Equal(t, "u1", disc.gotFollowingReq.GetUid())
	assert.Equal(t, []string{"u2", "u3"}, disc.gotFollowingReq.GetFollowingIds(), "全量关注注入（封顶内）")
	assert.Equal(t, "0", disc.gotFollowingReq.GetCursor().GetStr())
	require.Len(t, resp.GetPosts(), 1)
	assert.Equal(t, "5", resp.GetCursor().GetStr())
}

// TestBFFFeedAuth 未登录拒绝（三条流同构，取一代表）
func TestBFFFeedAuth(t *testing.T) {
	u := newBFFForFeedTest(&fakeUserClient{}, &fakeSearchPostClient{}, &fakeFeedDiscoverClient{})
	resp, err := u.GetHotPosts(context.Background(), &bffv1.GetHotPostsRequest{})
	require.NoError(t, err)
	assert.True(t, resp.GetFail())
}

// TestBFFFeedDownstreamError 下游连接错误 → InternalError（不再透传 nil resp）
func TestBFFFeedDownstreamError(t *testing.T) {
	u := newBFFForFeedTest(&fakeUserClient{}, &fakeSearchPostClient{}, &fakeFeedDiscoverClient{
		rpcErr: errors.New("connect refused"),
	})
	resp, err := u.GetRecommendFeed(ctxWithUID("u1"), &bffv1.GetRecommendFeedRequest{})
	require.NoError(t, err)
	assert.True(t, resp.GetFail())
}
