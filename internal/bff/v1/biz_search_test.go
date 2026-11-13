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
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDiscoverClient discover 域被测路径（SearchPosts），支持业务失败（failErr）与连接层错误（rpcErr）注入
type fakeDiscoverClient struct {
	discoverv1connect.DiscoverServiceClient

	hits    []*discoverv1.SearchHit
	cursor  *commonv1.Cursor
	failErr *commonv1.Error
	rpcErr  error
	gotReq  *discoverv1.SearchPostsRequest
}

func (f *fakeDiscoverClient) SearchPosts(_ context.Context, req *connect.Request[discoverv1.SearchPostsRequest]) (*connect.Response[discoverv1.SearchPostsResponse], error) {
	f.gotReq = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	if f.failErr != nil {
		return connect.NewResponse(&discoverv1.SearchPostsResponse{Fail: true, Err: f.failErr}), nil
	}
	return connect.NewResponse(&discoverv1.SearchPostsResponse{Hits: f.hits, Cursor: f.cursor}), nil
}

// fakeSearchPostClient 扩展 fakePostClient 支持面客批量回查 ViewPostsByIDs。
// 独立类型以保持 fakePostClient 不实现该方法的既有回归守卫（见 TestBFFGetMyPosts 注释）
type fakeSearchPostClient struct {
	fakePostClient

	viewResults []*postv1.PostBriefResult
	viewRPCErr  error
	gotViewReq  *postv1.ViewPostsByIDsRequest
}

func (f *fakeSearchPostClient) ViewPostsByIDs(_ context.Context, req *connect.Request[postv1.ViewPostsByIDsRequest]) (*connect.Response[postv1.ViewPostsByIDsResponse], error) {
	f.gotViewReq = req.Msg
	if f.viewRPCErr != nil {
		return nil, f.viewRPCErr
	}
	return connect.NewResponse(&postv1.ViewPostsByIDsResponse{Results: f.viewResults}), nil
}

func newBFFForSearchTest(user *fakeUserClient, media *fakeMediaClient, post *fakeSearchPostClient, discover *fakeDiscoverClient) *BFFUsecaseImpl {
	return &BFFUsecaseImpl{
		Repo: &DownstreamRepo{User: user, Media: media, Post: post, Discover: discover},
		MQ:   &fakeMQClient{},
		log:  slog.Default(),
	}
}

// visibleBrief 构造可见（PUBLISHED）帖子的面客批量查看结果（含首图封面）
func visibleBrief(id, author, title string) *postv1.PostBriefResult {
	return &postv1.PostBriefResult{
		PostId: id,
		Post: &postv1.PostBrief{
			PostId:    id,
			Title:     title,
			AuthorId:  author,
			CreatedAt: "1700000000123",
			Stat:      &postv1.PostStat{LikeCount: 5},
			Status:    postv1.PostStatus_POST_STATUS_PUBLISHED,
			Cover:     &mediav1.MediaFile{AssetId: "c-" + id, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
		},
	}
}

// invisibleBrief 不可见结果（ViewPostsByIDs 口径：fail + NotFound，不暴露状态存在性）
func invisibleBrief(id string) *postv1.PostBriefResult {
	return &postv1.PostBriefResult{PostId: id, Fail: true, Err: errmsg.NotFound}
}

// TestBFFSearchPosts 搜索聚合：命中顺序保持 + 回查组装 + 游标/请求透传
func TestBFFSearchPosts(t *testing.T) {
	t.Run("成功：命中顺序保持 + 卡片组装 + 透传", func(t *testing.T) {
		disc := &fakeDiscoverClient{
			hits: []*discoverv1.SearchHit{
				{PostId: "p1", Score: 0.9},
				{PostId: "p2", Score: 0.8},
			},
			cursor: &commonv1.Cursor{Str: "2", Limit: 10},
		}
		// 回查结果故意乱序（ViewPostsByIDs 不保证序），断言面客序 = 命中序
		post := &fakeSearchPostClient{viewResults: []*postv1.PostBriefResult{
			visibleBrief("p2", "u2", "第二"),
			visibleBrief("p1", "u1", "第一"),
		}}
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
			{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "a"}},
			{Uid: "u2", Brief: &userv1.UserBrief{Uid: "u2", Name: "b"}},
		}}
		u := newBFFForSearchTest(user, &fakeMediaClient{}, post, disc)

		resp, err := u.SearchPosts(ctxWithUID("u1"), &bffv1.SearchPostsRequest{
			Keyword: "关键词",
			Sort:    discoverv1.SearchSort_SEARCH_SORT_LATEST,
			Cursor:  &commonv1.Cursor{Str: "0", Limit: 10},
		})
		require.NoError(t, err)
		require.False(t, resp.Fail)

		// 请求透传
		require.NotNil(t, disc.gotReq)
		assert.Equal(t, "关键词", disc.gotReq.GetKeyword())
		assert.Equal(t, discoverv1.SearchSort_SEARCH_SORT_LATEST, disc.gotReq.GetSort())
		assert.Equal(t, "0", disc.gotReq.GetCursor().GetStr())
		require.NotNil(t, post.gotViewReq)
		assert.ElementsMatch(t, []string{"p1", "p2"}, post.gotViewReq.GetPostIds())

		// 命中顺序保持（p1 在前）
		require.Len(t, resp.GetPosts(), 2)
		assert.Equal(t, "p1", resp.GetPosts()[0].GetPostId())
		assert.Equal(t, "第一", resp.GetPosts()[0].GetTitle())
		assert.Equal(t, "p2", resp.GetPosts()[1].GetPostId())
		assert.Equal(t, "c-p1", resp.GetPosts()[0].GetCover().GetAssetId(), "封面 = 首张媒体")
		assert.Equal(t, "a", resp.GetPosts()[0].GetAuthor().GetName())
		assert.Equal(t, int64(5), resp.GetPosts()[0].GetStat().GetLikeCount())
		assert.Equal(t, "2", resp.GetCursor().GetStr(), "游标透传")
	})

	t.Run("同步窗口内非 PUBLISHED 命中被过滤", func(t *testing.T) {
		disc := &fakeDiscoverClient{hits: []*discoverv1.SearchHit{{PostId: "p1"}, {PostId: "p2"}}}
		post := &fakeSearchPostClient{viewResults: []*postv1.PostBriefResult{
			visibleBrief("p1", "u1", "正常"),
			invisibleBrief("p2"),
		}}
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
			{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "a"}},
		}}
		u := newBFFForSearchTest(user, &fakeMediaClient{}, post, disc)

		resp, err := u.SearchPosts(ctxWithUID("u1"), &bffv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		require.Len(t, resp.GetPosts(), 1, "已删命中不面客")
		assert.Equal(t, "p1", resp.GetPosts()[0].GetPostId())
	})

	t.Run("整页被过滤：按翻页到底处理", func(t *testing.T) {
		disc := &fakeDiscoverClient{hits: []*discoverv1.SearchHit{{PostId: "p1"}}, cursor: &commonv1.Cursor{Str: "1"}}
		post := &fakeSearchPostClient{viewResults: []*postv1.PostBriefResult{invisibleBrief("p1")}}
		u := newBFFForSearchTest(&fakeUserClient{}, &fakeMediaClient{}, post, disc)

		resp, err := u.SearchPosts(ctxWithUID("u1"), &bffv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Empty(t, resp.GetPosts())
		assert.Empty(t, resp.GetCursor().GetStr(), "空页即到底，不带游标")
	})

	t.Run("空命中：不回查下游", func(t *testing.T) {
		disc := &fakeDiscoverClient{}
		post := &fakeSearchPostClient{}
		u := newBFFForSearchTest(&fakeUserClient{}, &fakeMediaClient{}, post, disc)

		resp, err := u.SearchPosts(ctxWithUID("u1"), &bffv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Empty(t, resp.GetPosts())
		assert.Nil(t, post.gotViewReq)
	})

	t.Run("discover 业务失败透传 / 连接层错误转 InternalError", func(t *testing.T) {
		u := newBFFForSearchTest(&fakeUserClient{}, &fakeMediaClient{}, &fakeSearchPostClient{},
			&fakeDiscoverClient{failErr: errmsg.InvalidArgument})
		resp, err := u.SearchPosts(ctxWithUID("u1"), &bffv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, int64(1004), resp.GetErr().GetCode())

		u = newBFFForSearchTest(&fakeUserClient{}, &fakeMediaClient{}, &fakeSearchPostClient{},
			&fakeDiscoverClient{rpcErr: errors.New("down")})
		resp, err = u.SearchPosts(ctxWithUID("u1"), &bffv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, int64(1000), resp.GetErr().GetCode(), "连接层错误不外泄，转 InternalError")
	})

	t.Run("回查失败转 InternalError", func(t *testing.T) {
		disc := &fakeDiscoverClient{hits: []*discoverv1.SearchHit{{PostId: "p1"}}}
		post := &fakeSearchPostClient{viewRPCErr: errors.New("down")}
		u := newBFFForSearchTest(&fakeUserClient{}, &fakeMediaClient{}, post, disc)

		resp, err := u.SearchPosts(ctxWithUID("u1"), &bffv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, int64(1000), resp.GetErr().GetCode())
	})

	t.Run("未登录返回 Unauthorized", func(t *testing.T) {
		u := newBFFForSearchTest(&fakeUserClient{}, &fakeMediaClient{}, &fakeSearchPostClient{}, &fakeDiscoverClient{})
		resp, err := u.SearchPosts(context.Background(), &bffv1.SearchPostsRequest{Keyword: "kw"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, int64(1003), resp.GetErr().GetCode())
	})
}
