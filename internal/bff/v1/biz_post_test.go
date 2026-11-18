package v1

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/cache"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// fakePostClient post 域被测路径（草稿写入链路），支持业务失败（failErr）与连接层错误（rpcErr）注入
type fakePostClient struct {
	postv1connect.PostServiceClient

	post        *postv1.Post
	failErr     *commonv1.Error
	rpcErr      error
	oldAssetIDs []string

	gotCreateDraft       *postv1.CreateDraftRequest
	gotUpdateDraft       *postv1.UpdateDraftRequest
	gotUpdateDraftMedias *postv1.UpdateDraftMediasRequest
	gotPublishDraft      *postv1.PublishDraftRequest
	gotDeletePost        *postv1.DeletePostRequest

	viewPost       *postv1.Post
	viewPostErr    *commonv1.Error
	viewPostRPCErr error
	gotViewPost    *postv1.ViewPostRequest

	byAuthorPosts  map[string][]*postv1.PostBrief
	byAuthorCursor *commonv1.Cursor
	byAuthorErr    *commonv1.Error
	byAuthorRPCErr error
	gotByAuthor    []*postv1.ViewPostByAuthorRequest

	// 互动透传路径：共享错误注入 + 逐方法捕获入参
	interactFail   *commonv1.Error
	interactRPCErr error
	likeCount      int64
	favCount       int64
	listIDs        []string
	listCursor     *commonv1.Cursor

	gotLikePost   *postv1.LikePostRequest
	gotUnlikePost *postv1.UnlikePostRequest
	gotFavPost    *postv1.FavouritePostRequest
	gotUnfavPost  *postv1.UnfavouritePostRequest
	gotListLiked  *postv1.ListLikedPostIDsRequest
	gotListFav    *postv1.ListFavouritePostIDsRequest

	// 评论透传路径：共享错误注入 + 逐方法捕获入参
	commentFail   *commonv1.Error
	commentRPCErr error
	commentResp   *postv1.Comment
	commentCount  int64
	commentList   []*postv1.Comment
	commentCursor *commonv1.Cursor

	gotCreateComment *postv1.CreateCommentRequest
	gotReplyComment  *postv1.ReplyCommentRequest
	gotDeleteComment *postv1.DeleteCommentRequest
	gotListComments  *postv1.ListCommentsRequest
	gotListReplies   *postv1.ListRepliesRequest
}

func (f *fakePostClient) CreateDraft(_ context.Context, req *connect.Request[postv1.CreateDraftRequest]) (*connect.Response[postv1.CreateDraftResponse], error) {
	f.gotCreateDraft = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(&postv1.CreateDraftResponse{Fail: f.failErr != nil, Err: f.failErr, Draft: f.post}), nil
}

func (f *fakePostClient) UpdateDraft(_ context.Context, req *connect.Request[postv1.UpdateDraftRequest]) (*connect.Response[postv1.UpdateDraftResponse], error) {
	f.gotUpdateDraft = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(&postv1.UpdateDraftResponse{Fail: f.failErr != nil, Err: f.failErr, Post: f.post}), nil
}

func (f *fakePostClient) UpdateDraftMedias(_ context.Context, req *connect.Request[postv1.UpdateDraftMediasRequest]) (*connect.Response[postv1.UpdateDraftMediasResponse], error) {
	f.gotUpdateDraftMedias = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(&postv1.UpdateDraftMediasResponse{Fail: f.failErr != nil, Err: f.failErr, Post: f.post, OldAssetIds: f.oldAssetIDs}), nil
}

func (f *fakePostClient) PublishDraft(_ context.Context, req *connect.Request[postv1.PublishDraftRequest]) (*connect.Response[postv1.PublishDraftResponse], error) {
	f.gotPublishDraft = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(&postv1.PublishDraftResponse{Fail: f.failErr != nil, Err: f.failErr}), nil
}

func (f *fakePostClient) DeletePost(_ context.Context, req *connect.Request[postv1.DeletePostRequest]) (*connect.Response[postv1.DeletePostResponse], error) {
	f.gotDeletePost = req.Msg
	if f.rpcErr != nil {
		return nil, f.rpcErr
	}
	return connect.NewResponse(&postv1.DeletePostResponse{Fail: f.failErr != nil, Err: f.failErr}), nil
}

func (f *fakePostClient) ViewPost(_ context.Context, req *connect.Request[postv1.ViewPostRequest]) (*connect.Response[postv1.ViewPostResponse], error) {
	f.gotViewPost = req.Msg
	if f.viewPostRPCErr != nil {
		return nil, f.viewPostRPCErr
	}
	return connect.NewResponse(&postv1.ViewPostResponse{Fail: f.viewPostErr != nil, Err: f.viewPostErr, Post: f.viewPost}), nil
}

func (f *fakePostClient) ViewPostByAuthor(_ context.Context, req *connect.Request[postv1.ViewPostByAuthorRequest]) (*connect.Response[postv1.ViewPostByAuthorResponse], error) {
	f.gotByAuthor = append(f.gotByAuthor, req.Msg)
	if f.byAuthorRPCErr != nil {
		return nil, f.byAuthorRPCErr
	}
	if f.byAuthorErr != nil {
		return connect.NewResponse(&postv1.ViewPostByAuthorResponse{Fail: true, Err: f.byAuthorErr}), nil
	}
	return connect.NewResponse(&postv1.ViewPostByAuthorResponse{Posts: f.byAuthorPosts[req.Msg.GetAuthorId()], Cursor: f.byAuthorCursor}), nil
}

// newBFFForPostTest 组装带 post 域 fake 的 BFFUsecaseImpl
func newBFFForPostTest(user *fakeUserClient, media *fakeMediaClient, post *fakePostClient) *BFFUsecaseImpl {
	return newBFFForPostTestWithCache(user, media, post, &fakePostCache{})
}

// newBFFForPostTestWithCache 同 newBFFForPostTest，可注入浏览计数替身（GetPost 埋点断言用）
func newBFFForPostTestWithCache(user *fakeUserClient, media *fakeMediaClient, post *fakePostClient, pc *fakePostCache) *BFFUsecaseImpl {
	return &BFFUsecaseImpl{
		Repo:      &DownstreamRepo{User: user, Media: media, Post: post},
		MQ:        &fakeMQClient{},
		PostCache: pc,
		log:       slog.Default(),
	}
}

// ownedActiveBatch 两个归属 u1 的 ACTIVE 资产（批量归属校验用）
func ownedActiveBatch() []*mediav1.MediaAssetResult {
	active := &mediav1.MediaAsset{UploaderId: "u1", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}
	return []*mediav1.MediaAssetResult{
		{Id: "a1", Asset: active},
		{Id: "a2", Asset: active},
	}
}

// draftRefs 两个媒体槽位请求引用
func draftRefs() []*bffv1.AssetRef {
	return []*bffv1.AssetRef{
		{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
		{AssetId: "a2", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
	}
}

// draftPost 下游回显的草稿（DRAFT 状态 → 私有 TTL），全字段填充供全字段断言
func draftPost() *postv1.Post {
	return &postv1.Post{
		PostId:   "p1",
		Title:    "t",
		FullText: "body",
		Medias: []*mediav1.MediaFile{
			{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
			{AssetId: "a2", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
		},
		Metadata:  &commonv1.JsonStr{Payload: `{"k":1}`},
		PostStat:  &postv1.PostStat{LikeCount: 3, ViewCount: 5, CommentCount: 7, FavouriteCount: 9},
		CreatedAt: "1700000000000",
		UpdatedAt: "1700000001000",
		Status:    postv1.PostStatus_POST_STATUS_DRAFT,
	}
}

// TestBFFPostUnauthorized 会话缺失时帖子写入接口一律返回 Unauthorized（不透传下游）
func TestBFFPostUnauthorized(t *testing.T) {
	u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{})

	cases := []struct {
		name string
		call func() *commonv1.Error
	}{
		{"CreateDraft", func() *commonv1.Error {
			r, _ := u.CreateDraft(context.Background(), &bffv1.CreateDraftRequest{})
			return r.Err
		}},
		{"UpdateDraft", func() *commonv1.Error {
			r, _ := u.UpdateDraft(context.Background(), &bffv1.UpdateDraftRequest{})
			return r.Err
		}},
		{"UpdateDraftMedias", func() *commonv1.Error {
			r, _ := u.UpdateDraftMedias(context.Background(), &bffv1.UpdateDraftMediasRequest{})
			return r.Err
		}},
		{"PublishDraft", func() *commonv1.Error {
			r, _ := u.PublishDraft(context.Background(), &bffv1.PublishDraftRequest{})
			return r.Err
		}},
		{"DeletePost", func() *commonv1.Error {
			r, _ := u.DeletePost(context.Background(), &bffv1.DeletePostRequest{})
			return r.Err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, errmsg.Unauthorized.Code, c.call().GetCode())
		})
	}
}

// TestBFFCreateDraft 创建草稿：批量归属校验 + 挂载 +1 → post.CreateDraft → PostView 回显；下游失败回滚
func TestBFFCreateDraft(t *testing.T) {
	meta := &commonv1.JsonStr{Payload: `{"k":1}`}

	t.Run("资产归属不匹配 Forbidden，不调用下游", func(t *testing.T) {
		media := &fakeMediaClient{batch: []*mediav1.MediaAssetResult{
			{Id: "a1", Asset: &mediav1.MediaAsset{UploaderId: "u2", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}},
		}}
		post := &fakePostClient{}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.CreateDraft(ctxWithUID("u1"), &bffv1.CreateDraftRequest{Medias: []*bffv1.AssetRef{{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}}})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.Forbidden.Code, resp.Err.GetCode())
		assert.Nil(t, post.gotCreateDraft)
		assert.Empty(t, media.refCalls, "归属校验失败不得调整计数")
	})

	t.Run("资产非 ACTIVE InvalidMedia，不调用下游", func(t *testing.T) {
		media := &fakeMediaClient{batch: []*mediav1.MediaAssetResult{
			{Id: "a1", Asset: &mediav1.MediaAsset{UploaderId: "u1", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_UPLOADING}},
		}}
		post := &fakePostClient{}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.CreateDraft(ctxWithUID("u1"), &bffv1.CreateDraftRequest{Medias: []*bffv1.AssetRef{{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}}})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InvalidMedia.Code, resp.Err.GetCode())
		assert.Nil(t, post.gotCreateDraft)
		assert.Empty(t, media.refCalls)
	})

	t.Run("成功：挂载 +1 → uid 注入 + 文件回填 → PostView 回显（私有 TTL）", func(t *testing.T) {
		media := &fakeMediaClient{batch: ownedActiveBatch(), file: &mediav1.MediaFile{Base: &commonv1.BaseModel{Id: "f1"}}}
		post := &fakePostClient{post: draftPost()}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.CreateDraft(ctxWithUID("u1"), &bffv1.CreateDraftRequest{
			Title: "t", FullText: "body", Medias: draftRefs(), Metadata: meta,
		})
		require.NoError(t, err)
		require.False(t, resp.Fail)

		assert.Equal(t, []refCall{{id: "a1", delta: 1}, {id: "a2", delta: 1}}, media.refCalls, "每个媒体槽位同步 +1")
		require.NotNil(t, post.gotCreateDraft)
		assert.Equal(t, "u1", post.gotCreateDraft.GetUid(), "uid 必须来自会话")
		assert.Equal(t, "t", post.gotCreateDraft.GetTitle())
		assert.Equal(t, meta, post.gotCreateDraft.GetMetadata())
		require.Len(t, post.gotCreateDraft.GetMedias(), 2, "下游回填完整 MediaFile")

		require.NotNil(t, resp.GetPost())
		pv := resp.GetPost()
		// 全字段断言：PostView 逐字段回显
		assert.Equal(t, "p1", pv.GetPostId())
		assert.Equal(t, "t", pv.GetTitle())
		assert.Equal(t, "body", pv.GetFullText())
		assert.Equal(t, meta, pv.GetMetadata())
		assert.Equal(t, int64(3), pv.GetStat().GetLikeCount())
		assert.Equal(t, int64(5), pv.GetStat().GetViewCount())
		assert.Equal(t, int64(7), pv.GetStat().GetCommentCount())
		assert.Equal(t, int64(9), pv.GetStat().GetFavouriteCount())
		assert.Equal(t, "1700000000000", pv.GetCreatedAt())
		assert.Equal(t, "1700000001000", pv.GetUpdatedAt())
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, pv.GetStatus())
		require.Len(t, pv.GetMedias(), 2)
		assert.Equal(t, "https://signed/a1", pv.GetMedias()[0].GetUrl())
		require.NotEmpty(t, media.gotSignTTLs)
		assert.Equal(t, int64(24*3600), media.gotSignTTLs[0], "草稿媒体私有 TTL 24h")
	})

	t.Run("下游业务失败：错误透传 + 回滚挂载", func(t *testing.T) {
		media := &fakeMediaClient{batch: ownedActiveBatch(), file: &mediav1.MediaFile{}}
		post := &fakePostClient{failErr: errmsg.InvalidMedia}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.CreateDraft(ctxWithUID("u1"), &bffv1.CreateDraftRequest{Medias: draftRefs()})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InvalidMedia.Code, resp.Err.GetCode())
		assert.Equal(t, []string{"a1", "a2"}, derefSent(t, u), "下游失败回滚全部挂载槽位")
	})

	t.Run("下游连接层错误：InternalError + 回滚挂载", func(t *testing.T) {
		media := &fakeMediaClient{batch: ownedActiveBatch(), file: &mediav1.MediaFile{}}
		post := &fakePostClient{rpcErr: errors.New("dial tcp: connection refused")}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.CreateDraft(ctxWithUID("u1"), &bffv1.CreateDraftRequest{Medias: draftRefs()})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
		assert.Equal(t, []string{"a1", "a2"}, derefSent(t, u))
	})
}

// TestBFFUpdateDraft 更新草稿：可空字段透传 + PostView 回显，业务错误透传
func TestBFFUpdateDraft(t *testing.T) {
	t.Run("成功：uid 注入 + 部分更新透传", func(t *testing.T) {
		post := &fakePostClient{post: draftPost()}
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := u.UpdateDraft(ctxWithUID("u1"), &bffv1.UpdateDraftRequest{
			PostId: "p1", Title: wrapperspb.String("new title"),
		})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		require.NotNil(t, post.gotUpdateDraft)
		assert.Equal(t, "u1", post.gotUpdateDraft.GetUid())
		assert.Equal(t, "p1", post.gotUpdateDraft.GetPostId())
		assert.Equal(t, "new title", post.gotUpdateDraft.GetTitle().GetValue())
		assert.Nil(t, post.gotUpdateDraft.GetFullText(), "nil 字段不修改")
		assert.Equal(t, "p1", resp.GetPost().GetPostId())
	})

	t.Run("业务失败透传", func(t *testing.T) {
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{failErr: errmsg.NotEditable})

		resp, err := u.UpdateDraft(ctxWithUID("u1"), &bffv1.UpdateDraftRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.NotEditable.Code, resp.Err.GetCode())
	})
}

// TestBFFUpdateDraftMedias 媒体全量替换：先挂新 +1 → 下游替换 → 旧资产 MQ 卸载；失败回滚新挂载
func TestBFFUpdateDraftMedias(t *testing.T) {
	t.Run("成功：挂新 +1，旧资产发卸载消息", func(t *testing.T) {
		media := &fakeMediaClient{batch: ownedActiveBatch(), file: &mediav1.MediaFile{}}
		post := &fakePostClient{post: draftPost(), oldAssetIDs: []string{"old1", "old2"}}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.UpdateDraftMedias(ctxWithUID("u1"), &bffv1.UpdateDraftMediasRequest{PostId: "p1", Medias: draftRefs()})
		require.NoError(t, err)
		require.False(t, resp.Fail)

		assert.Equal(t, []refCall{{id: "a1", delta: 1}, {id: "a2", delta: 1}}, media.refCalls, "新媒体槽位同步 +1")
		require.NotNil(t, post.gotUpdateDraftMedias)
		assert.Equal(t, "u1", post.gotUpdateDraftMedias.GetUid())
		assert.Len(t, post.gotUpdateDraftMedias.GetMedias(), 2)
		assert.Equal(t, []string{"old1", "old2"}, derefSent(t, u), "旧资产卸载走 MQ（中间产物不外泄）")
	})

	t.Run("下游失败：回滚新挂载，不卸载旧资产", func(t *testing.T) {
		media := &fakeMediaClient{batch: ownedActiveBatch(), file: &mediav1.MediaFile{}}
		post := &fakePostClient{failErr: errmsg.NotEditable, oldAssetIDs: []string{"old1"}}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.UpdateDraftMedias(ctxWithUID("u1"), &bffv1.UpdateDraftMediasRequest{PostId: "p1", Medias: draftRefs()})
		require.NoError(t, err)
		assert.Equal(t, errmsg.NotEditable.Code, resp.Err.GetCode())
		assert.Equal(t, []string{"a1", "a2"}, derefSent(t, u), "只回滚新挂载，不得动旧资产")
	})

	t.Run("同资产多槽位：逐槽 +1，旧槽位逐个卸载（计数守恒）", func(t *testing.T) {
		media := &fakeMediaClient{
			batch: []*mediav1.MediaAssetResult{{Id: "a1", Asset: &mediav1.MediaAsset{UploaderId: "u1", Status: mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE}}},
			file:  &mediav1.MediaFile{},
		}
		post := &fakePostClient{post: draftPost(), oldAssetIDs: []string{"a1", "a1"}}
		u := newBFFForPostTest(&fakeUserClient{}, media, post)

		resp, err := u.UpdateDraftMedias(ctxWithUID("u1"), &bffv1.UpdateDraftMediasRequest{PostId: "p1", Medias: []*bffv1.AssetRef{
			{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
			{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
		}})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Equal(t, []refCall{{id: "a1", delta: 1}, {id: "a1", delta: 1}}, media.refCalls,
			"同资产多变体各占一槽，逐槽 +1")
		assert.Equal(t, []string{"a1", "a1"}, derefSent(t, u), "旧两槽逐个卸载，计数与新槽对齐")
	})
}

// TestBFFPublishDraft 发布草稿：uid 注入透传，错误分类
func TestBFFPublishDraft(t *testing.T) {
	t.Run("成功透传", func(t *testing.T) {
		post := &fakePostClient{}
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := u.PublishDraft(ctxWithUID("u1"), &bffv1.PublishDraftRequest{PostId: "p1"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		require.NotNil(t, post.gotPublishDraft)
		assert.Equal(t, "u1", post.gotPublishDraft.GetUid())
		assert.Equal(t, "p1", post.gotPublishDraft.GetPostId())
	})

	t.Run("业务失败透传 / 连接层错误归为 InternalError", func(t *testing.T) {
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{failErr: errmsg.NotEditable})
		resp, err := u.PublishDraft(ctxWithUID("u1"), &bffv1.PublishDraftRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.NotEditable.Code, resp.Err.GetCode())

		u = newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{rpcErr: errors.New("dial fail")})
		resp, err = u.PublishDraft(ctxWithUID("u1"), &bffv1.PublishDraftRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestBFFDeletePost 删除帖子：uid 注入透传 + 错误分类
// （资产计数由删帖消息链路回收，BFF 不发卸载消息）
func TestBFFDeletePost(t *testing.T) {
	t.Run("成功：uid 注入透传，无卸载消息", func(t *testing.T) {
		post := &fakePostClient{}
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := u.DeletePost(ctxWithUID("u1"), &bffv1.DeletePostRequest{PostId: "p1"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Nil(t, resp.Err)
		require.NotNil(t, post.gotDeletePost)
		assert.Equal(t, "u1", post.gotDeletePost.GetUid())
		assert.Equal(t, "p1", post.gotDeletePost.GetPostId())
		assert.Empty(t, derefSent(t, u), "计数回收由删帖消息链路承担，BFF 不发卸载消息")
	})

	t.Run("业务失败透传 / 连接层错误归为 InternalError", func(t *testing.T) {
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{failErr: errmsg.NotFound})
		resp, err := u.DeletePost(ctxWithUID("u1"), &bffv1.DeletePostRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.NotFound.Code, resp.Err.GetCode())

		u = newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{rpcErr: errors.New("dial fail")})
		resp, err = u.DeletePost(ctxWithUID("u1"), &bffv1.DeletePostRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestMediaTTLFor URL TTL 分级：PUBLISHED 公开 7d，其余私有 24h
func TestMediaTTLFor(t *testing.T) {
	assert.Equal(t, publicMediaTTL, mediaTTLFor(postv1.PostStatus_POST_STATUS_PUBLISHED))
	assert.Equal(t, privateMediaTTL, mediaTTLFor(postv1.PostStatus_POST_STATUS_DRAFT))
	assert.Equal(t, privateMediaTTL, mediaTTLFor(postv1.PostStatus_POST_STATUS_AUDITING))
}

// ==================== 帖子查询（M2 读路径） ====================

// TestBFFGetPost 帖子详情：可见性透传 + 作者/关系并行聚合 + 分级签发；关系 NotFound 降级未关注
func TestBFFGetPost(t *testing.T) {
	authorBrief := &userv1.UserBrief{Uid: "u2", Name: "bob", Avatar: &mediav1.MediaFile{AssetId: "av1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}}
	viewPost := &postv1.Post{
		PostId:    "p1",
		Title:     "t",
		FullText:  "body",
		Medias:    []*mediav1.MediaFile{{AssetId: "a1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}},
		Metadata:  &commonv1.JsonStr{Payload: `{"k":1}`},
		AuthorId:  "u2",
		PostStat:  &postv1.PostStat{LikeCount: 1, ViewCount: 9, CommentCount: 2, FavouriteCount: 4},
		CreatedAt: "1700000000000",
		UpdatedAt: "1700000001000",
		Status:    postv1.PostStatus_POST_STATUS_PUBLISHED,
	}

	t.Run("无会话返回 Unauthorized", func(t *testing.T) {
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{})
		resp, err := u.GetPost(context.Background(), &bffv1.GetPostRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.Unauthorized.Code, resp.Err.GetCode())
	})

	t.Run("下游业务失败透传（Banned）", func(t *testing.T) {
		post := &fakePostClient{viewPostErr: errmsg.Banned}
		u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.Banned.Code, resp.Err.GetCode())
	})

	t.Run("下游裁决透传：软删 NotFound / 非作者看草稿 Unauthorized", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			err  *commonv1.Error
		}{
			{"软删对外隐藏透传 NotFound", errmsg.NotFound},
			{"非作者看草稿透传 Unauthorized", errmsg.Unauthorized},
		} {
			post := &fakePostClient{viewPostErr: tc.err}
			u := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

			resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "p1"})
			require.NoError(t, err)
			assert.True(t, resp.Fail)
			assert.Equal(t, tc.err.GetCode(), resp.Err.GetCode(), tc.name)
		}
	})

	t.Run("成功：作者/关系聚合 + PostDetail 组装", func(t *testing.T) {
		user := &fakeUserClient{
			briefResults: []*userv1.UserBriefResult{{Uid: "u2", Brief: authorBrief}},
			relation:     &userv1.GetFollowingRelationResponse{IsFollowing: true, IsFollower: false},
		}
		media := &fakeMediaClient{}
		post := &fakePostClient{viewPost: viewPost}
		u := newBFFForPostTest(user, media, post)

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "p1"})
		require.NoError(t, err)
		require.False(t, resp.Fail)

		require.NotNil(t, post.gotViewPost)
		assert.Equal(t, "u1", post.gotViewPost.GetViewerId(), "viewer 必须来自会话")
		assert.Equal(t, "p1", post.gotViewPost.GetPostId())

		d := resp.GetPost()
		// 全字段断言：PostDetail 逐字段回显
		assert.Equal(t, "p1", d.GetPostId())
		assert.Equal(t, "t", d.GetTitle())
		assert.Equal(t, "body", d.GetFullText())
		assert.Equal(t, &commonv1.JsonStr{Payload: `{"k":1}`}, d.GetMetadata())
		assert.Equal(t, int64(1), d.GetStat().GetLikeCount())
		assert.Equal(t, int64(9), d.GetStat().GetViewCount())
		assert.Equal(t, int64(2), d.GetStat().GetCommentCount())
		assert.Equal(t, int64(4), d.GetStat().GetFavouriteCount())
		assert.Equal(t, "1700000000000", d.GetCreatedAt())
		assert.Equal(t, "1700000001000", d.GetUpdatedAt())
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, d.GetStatus())
		assert.Equal(t, "u2", d.GetAuthor().GetUid())
		assert.Equal(t, "bob", d.GetAuthor().GetName())
		assert.Equal(t, "av1", d.GetAuthor().GetAvatar().GetAssetId())
		assert.True(t, d.GetRelation().GetIsFollowing())
		assert.False(t, d.GetRelation().GetIsFollower())
		require.Len(t, d.GetMedias(), 1)
		assert.Equal(t, "a1", d.GetMedias()[0].GetAssetId())
		assert.Equal(t, "https://signed/a1", d.GetMedias()[0].GetUrl())
		require.Len(t, media.gotSignTTLs, 2)
		assert.Equal(t, int64(7*24*3600), media.gotSignTTLs[0], "作者头像公开 TTL 7d")
		assert.Equal(t, int64(7*24*3600), media.gotSignTTLs[1], "已发布详情媒体公开 TTL 7d")
	})

	t.Run("草稿详情私有 TTL；关系 NotFound 降级未关注", func(t *testing.T) {
		draft := &postv1.Post{PostId: "p2", AuthorId: "u2", Medias: []*mediav1.MediaFile{{AssetId: "a2", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}}, Status: postv1.PostStatus_POST_STATUS_DRAFT}
		user := &fakeUserClient{
			briefResults: []*userv1.UserBriefResult{{Uid: "u2", Brief: authorBrief}},
			relationErr:  errmsg.NotFound,
		}
		media := &fakeMediaClient{}
		post := &fakePostClient{viewPost: draft}
		u := newBFFForPostTest(user, media, post)

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "p2"})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.False(t, resp.GetPost().GetRelation().GetIsFollowing(), "无关系记录按未关注展示")
		require.Len(t, media.gotSignTTLs, 2, "头像与媒体分批签发")
		assert.Equal(t, int64(7*24*3600), media.gotSignTTLs[0], "作者头像公开 TTL 7d")
		assert.Equal(t, int64(24*3600), media.gotSignTTLs[1], "草稿媒体私有 TTL 24h")
	})

	t.Run("关系查询其他错误透传", func(t *testing.T) {
		user := &fakeUserClient{
			briefResults: []*userv1.UserBriefResult{{Uid: "u2", Brief: authorBrief}},
			relationErr:  errmsg.InternalError,
		}
		u := newBFFForPostTest(user, &fakeMediaClient{}, &fakePostClient{viewPost: viewPost})

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.True(t, resp.Fail)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})

	t.Run("作者查无透传错误码", func(t *testing.T) {
		user := &fakeUserClient{
			briefResults: []*userv1.UserBriefResult{{Uid: "u2", Fail: true, Err: errmsg.NotFound}},
			relation:     &userv1.GetFollowingRelationResponse{},
		}
		u := newBFFForPostTest(user, &fakeMediaClient{}, &fakePostClient{viewPost: viewPost})

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.NotFound.Code, resp.Err.GetCode(), "作者是详情必需项，失败不静默")
	})

	t.Run("聚合连接层错误归为 InternalError", func(t *testing.T) {
		user := &fakeUserClient{briefRPCErr: errors.New("dial fail")}
		u := newBFFForPostTest(user, &fakeMediaClient{}, &fakePostClient{viewPost: viewPost})

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "p1"})
		require.NoError(t, err)
		assert.Equal(t, errmsg.InternalError.Code, resp.Err.GetCode())
	})
}

// TestBFFGetMyPosts 我的帖子列表：状态随 PostBrief 返回 + 封面分级签发 + 作者一次取齐
// （回归：postCards 直读 PostBrief.status，fakePostClient 未实现 GetPostsByIDs，
// 若实现退回二次查询状态会因未覆写方法 panic 而失败）
func TestBFFGetMyPosts(t *testing.T) {
	briefs := []*postv1.PostBrief{
		{PostId: "p1", Title: "pub", AuthorId: "u1", Status: postv1.PostStatus_POST_STATUS_PUBLISHED, Cover: &mediav1.MediaFile{AssetId: "c1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}},
		{PostId: "p2", Title: "draft", AuthorId: "u1", Status: postv1.PostStatus_POST_STATUS_DRAFT, Cover: &mediav1.MediaFile{AssetId: "c2", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}},
	}

	t.Run("成功：状态透传 + 分级签发 + 游标透传", func(t *testing.T) {
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "me"}}}}
		media := &fakeMediaClient{}
		post := &fakePostClient{
			byAuthorPosts:  map[string][]*postv1.PostBrief{"u1": briefs},
			byAuthorCursor: &commonv1.Cursor{Str: "next", Limit: 10},
		}
		u := newBFFForPostTest(user, media, post)

		resp, err := u.GetMyPosts(ctxWithUID("u1"), &bffv1.GetMyPostsRequest{Cursor: &commonv1.Cursor{Str: "cur", Limit: 10}})
		require.NoError(t, err)
		require.False(t, resp.Fail)

		require.NotNil(t, post.gotByAuthor)
		require.Len(t, post.gotByAuthor, 1)
		assert.Equal(t, "u1", post.gotByAuthor[0].GetViewerId(), "作者视角 viewer=author=会话用户")
		assert.Equal(t, "u1", post.gotByAuthor[0].GetAuthorId())
		assert.Equal(t, "cur", post.gotByAuthor[0].GetCursor().GetStr(), "游标透传")
		assert.Equal(t, "next", resp.GetCursor().GetStr())

		require.Len(t, resp.GetPosts(), 2)
		// 全字段断言：PostCard 逐字段回显
		c0, c1 := resp.GetPosts()[0], resp.GetPosts()[1]
		assert.Equal(t, "p1", c0.GetPostId())
		assert.Equal(t, "pub", c0.GetTitle())
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, c0.GetStatus())
		assert.Equal(t, "me", c0.GetAuthor().GetName())
		assert.Equal(t, "u1", c0.GetAuthor().GetUid())
		assert.Equal(t, "https://signed/c1", c0.GetCover().GetUrl())
		assert.Equal(t, "c1", c0.GetCover().GetAssetId())
		assert.Equal(t, "p2", c1.GetPostId())
		assert.Equal(t, "draft", c1.GetTitle())
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, c1.GetStatus())
		assert.Equal(t, "https://signed/c2", c1.GetCover().GetUrl())
		require.Len(t, media.gotSignTTLs, 2, "公开/私有各一批签发")
		assert.Equal(t, int64(7*24*3600), media.gotSignTTLs[0], "已发布封面公开 TTL")
		assert.Equal(t, int64(24*3600), media.gotSignTTLs[1], "草稿封面私有 TTL")
		require.Len(t, user.gotBriefReqs, 1, "作者仅批量取一次")
	})

	t.Run("空列表：无卡片不签发，游标透传", func(t *testing.T) {
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1"}}}}
		media := &fakeMediaClient{}
		post := &fakePostClient{
			byAuthorPosts:  map[string][]*postv1.PostBrief{"u1": {}},
			byAuthorCursor: &commonv1.Cursor{},
		}
		u := newBFFForPostTest(user, media, post)

		resp, err := u.GetMyPosts(ctxWithUID("u1"), &bffv1.GetMyPostsRequest{})
		require.NoError(t, err)
		require.False(t, resp.Fail)
		assert.Empty(t, resp.GetPosts())
		assert.Empty(t, media.gotSignReqs, "空列表不调签发")
	})
}

// TestBFFAuthorViews 作者简视图批量：去重 + 空串过滤 + 部分失败降级 + 全失败返回空
func TestBFFAuthorViews(t *testing.T) {
	t.Run("去重 + 空串过滤 + 部分失败降级", func(t *testing.T) {
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
			{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "a"}},
			{Uid: "u2", Fail: true, Err: errmsg.NotFound},
		}}
		u := newBFFForPostTest(user, &fakeMediaClient{}, &fakePostClient{})

		views, errCode := u.authorViews(context.Background(), []string{"u1", "u2", "u1", ""})
		require.Nil(t, errCode)
		require.Len(t, views, 1, "失败项跳过，成功项保留（列表可用性优先）")
		assert.Equal(t, "a", views["u1"].GetName())
		require.Len(t, user.gotBriefReqs, 1)
		assert.ElementsMatch(t, []string{"u1", "u2"}, user.gotBriefReqs[0].GetUids(), "去重 + 空串过滤后批量取")
	})

	t.Run("全失败返回空 map 不打挂", func(t *testing.T) {
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{{Uid: "u2", Fail: true, Err: errmsg.NotFound}}}
		u := newBFFForPostTest(user, &fakeMediaClient{}, &fakePostClient{})

		views, errCode := u.authorViews(context.Background(), []string{"u2"})
		require.Nil(t, errCode)
		assert.Empty(t, views)
	})
}

// TestBFFGetUserPosts 他人帖子列表：status 取自 PostBrief（恒 PUBLISHED）+ 公开 TTL + target 作者
func TestBFFGetUserPosts(t *testing.T) {
	user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{{Uid: "u2", Brief: &userv1.UserBrief{Uid: "u2", Name: "bob"}}}}
	media := &fakeMediaClient{}
	post := &fakePostClient{
		byAuthorPosts: map[string][]*postv1.PostBrief{
			"u2": {{PostId: "p9", Title: "hi", AuthorId: "u2", Status: postv1.PostStatus_POST_STATUS_PUBLISHED,
				Stat: &postv1.PostStat{ViewCount: 8}, CreatedAt: "1700000002000",
				Cover: &mediav1.MediaFile{AssetId: "c9", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}}},
		},
	}
	u := newBFFForPostTest(user, media, post)

	resp, err := u.GetUserPosts(ctxWithUID("u1"), &bffv1.GetUserPostsRequest{Uid: "u2"})
	require.NoError(t, err)
	require.False(t, resp.Fail)
	assert.Nil(t, resp.Err)

	require.Len(t, post.gotByAuthor, 1)
	assert.Equal(t, "u1", post.gotByAuthor[0].GetViewerId(), "viewer 为会话用户")
	assert.Equal(t, "u2", post.gotByAuthor[0].GetAuthorId(), "author 为目标用户")

	require.Len(t, resp.GetPosts(), 1)
	c := resp.GetPosts()[0]
	// 全字段断言：PostCard 逐字段回显
	assert.Equal(t, "p9", c.GetPostId())
	assert.Equal(t, "hi", c.GetTitle())
	assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, c.GetStatus())
	assert.Equal(t, "bob", c.GetAuthor().GetName())
	assert.Equal(t, "u2", c.GetAuthor().GetUid())
	assert.Equal(t, int64(8), c.GetStat().GetViewCount())
	assert.Equal(t, "1700000002000", c.GetCreatedAt())
	assert.Equal(t, "c9", c.GetCover().GetAssetId())
	require.NotEmpty(t, media.gotSignTTLs)
	assert.Equal(t, int64(7*24*3600), media.gotSignTTLs[0], "已发布封面公开 TTL")
}

// ==================== 互动（点赞/收藏） ====================

// 互动透传 fake：uid 由 BFF 会话注入，响应回传计数 + 态位

func (f *fakePostClient) LikePost(_ context.Context, req *connect.Request[postv1.LikePostRequest]) (*connect.Response[postv1.LikePostResponse], error) {
	f.gotLikePost = req.Msg
	if f.interactRPCErr != nil {
		return nil, f.interactRPCErr
	}
	return connect.NewResponse(&postv1.LikePostResponse{Fail: f.interactFail != nil, Err: f.interactFail, LikeCount: f.likeCount, Liked: true}), nil
}

func (f *fakePostClient) UnlikePost(_ context.Context, req *connect.Request[postv1.UnlikePostRequest]) (*connect.Response[postv1.UnlikePostResponse], error) {
	f.gotUnlikePost = req.Msg
	if f.interactRPCErr != nil {
		return nil, f.interactRPCErr
	}
	return connect.NewResponse(&postv1.UnlikePostResponse{Fail: f.interactFail != nil, Err: f.interactFail, LikeCount: f.likeCount, Liked: false}), nil
}

func (f *fakePostClient) FavouritePost(_ context.Context, req *connect.Request[postv1.FavouritePostRequest]) (*connect.Response[postv1.FavouritePostResponse], error) {
	f.gotFavPost = req.Msg
	if f.interactRPCErr != nil {
		return nil, f.interactRPCErr
	}
	return connect.NewResponse(&postv1.FavouritePostResponse{Fail: f.interactFail != nil, Err: f.interactFail, FavouriteCount: f.favCount, Favourited: true}), nil
}

func (f *fakePostClient) UnfavouritePost(_ context.Context, req *connect.Request[postv1.UnfavouritePostRequest]) (*connect.Response[postv1.UnfavouritePostResponse], error) {
	f.gotUnfavPost = req.Msg
	if f.interactRPCErr != nil {
		return nil, f.interactRPCErr
	}
	return connect.NewResponse(&postv1.UnfavouritePostResponse{Fail: f.interactFail != nil, Err: f.interactFail, FavouriteCount: f.favCount, Favourited: false}), nil
}

func (f *fakePostClient) ListLikedPostIDs(_ context.Context, req *connect.Request[postv1.ListLikedPostIDsRequest]) (*connect.Response[postv1.ListLikedPostIDsResponse], error) {
	f.gotListLiked = req.Msg
	if f.interactRPCErr != nil {
		return nil, f.interactRPCErr
	}
	return connect.NewResponse(&postv1.ListLikedPostIDsResponse{Fail: f.interactFail != nil, Err: f.interactFail, PostIds: f.listIDs, Cursor: f.listCursor}), nil
}

func (f *fakePostClient) ListFavouritePostIDs(_ context.Context, req *connect.Request[postv1.ListFavouritePostIDsRequest]) (*connect.Response[postv1.ListFavouritePostIDsResponse], error) {
	f.gotListFav = req.Msg
	if f.interactRPCErr != nil {
		return nil, f.interactRPCErr
	}
	return connect.NewResponse(&postv1.ListFavouritePostIDsResponse{Fail: f.interactFail != nil, Err: f.interactFail, PostIds: f.listIDs, Cursor: f.listCursor}), nil
}

// TestBFFInteractions 互动透传：uid 会话注入 + 计数/态位回传 + 错误映射 + 判态列表透传
func TestBFFInteractions(t *testing.T) {
	t.Run("LikePost：uid 来自会话，计数/态位透传", func(t *testing.T) {
		post := &fakePostClient{likeCount: 8}
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := bff.LikePost(ctxWithUID("u1"), &bffv1.LikePostRequest{PostId: "2001"})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, int64(8), resp.GetLikeCount())
		assert.True(t, resp.GetLiked())
		require.NotNil(t, post.gotLikePost)
		assert.Equal(t, "u1", post.gotLikePost.GetUid(), "uid 必须来自会话注入，请求体不带 uid")
		assert.Equal(t, "2001", post.gotLikePost.GetPostId())
	})

	t.Run("无会话 → Unauthorized", func(t *testing.T) {
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, &fakePostClient{})
		resp, err := bff.FavouritePost(context.Background(), &bffv1.FavouritePostRequest{PostId: "2001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.Unauthorized.Code, resp.GetErr().GetCode())
	})

	t.Run("业务失败透传（FavouriteLimitExceeded）", func(t *testing.T) {
		post := &fakePostClient{interactFail: errmsg.FavouriteLimitExceeded}
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := bff.FavouritePost(ctxWithUID("u1"), &bffv1.FavouritePostRequest{PostId: "2001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.FavouriteLimitExceeded.Code, resp.GetErr().GetCode())
	})

	t.Run("连接层错误 → InternalError（不外泄）", func(t *testing.T) {
		post := &fakePostClient{interactRPCErr: errors.New("boom")}
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := bff.UnlikePost(ctxWithUID("u1"), &bffv1.UnlikePostRequest{PostId: "2001"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.InternalError.Code, resp.GetErr().GetCode())
	})

	t.Run("GetMyLikedPostIDs：uid 注入 + 列表/游标透传", func(t *testing.T) {
		post := &fakePostClient{
			listIDs:    []string{"2001", "2002"},
			listCursor: &commonv1.Cursor{Str: "1000", Limit: 20},
		}
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := bff.GetMyLikedPostIDs(ctxWithUID("u1"), &bffv1.GetMyLikedPostIDsRequest{})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, []string{"2001", "2002"}, resp.GetPostIds())
		assert.Equal(t, "1000", resp.GetCursor().GetStr())
		require.NotNil(t, post.gotListLiked)
		assert.Equal(t, "u1", post.gotListLiked.GetUid())
	})

	t.Run("GetMyFavouritePostIDs：同构透传", func(t *testing.T) {
		post := &fakePostClient{listIDs: []string{"2003"}}
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := bff.GetMyFavouritePostIDs(ctxWithUID("u1"), &bffv1.GetMyFavouritePostIDsRequest{})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, []string{"2003"}, resp.GetPostIds())
		require.NotNil(t, post.gotListFav)
		assert.Equal(t, "u1", post.gotListFav.GetUid())
	})
}

// ==================== 浏览计数埋点（PV 近似） ====================

// fakePostCache 浏览计数替身：记录 IncrPostViewCount 调用，可注入错误
type fakePostCache struct {
	cache.PostEntityCache
	gotIncr []int64
	incrErr error
}

func (f *fakePostCache) IncrPostViewCount(_ context.Context, id int64) error {
	f.gotIncr = append(f.gotIncr, id)
	return f.incrErr
}

// TestBFFGetPostViewCount 埋点口径：他人访问 +1、作者自访不计、Redis 故障降级不打挂详情页
func TestBFFGetPostViewCount(t *testing.T) {
	newPost := func() *postv1.Post {
		return &postv1.Post{
			PostId:   "42",
			Title:    "t",
			FullText: "body",
			AuthorId: "u2",
			Status:   postv1.PostStatus_POST_STATUS_PUBLISHED,
		}
	}
	newUser := func() *fakeUserClient {
		return &fakeUserClient{
			briefResults: []*userv1.UserBriefResult{{Uid: "u2", Brief: &userv1.UserBrief{
				Uid: "u2", Avatar: &mediav1.MediaFile{AssetId: "av1", Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
			}}},
			relation: &userv1.GetFollowingRelationResponse{},
		}
	}

	t.Run("他人访问：计数 +1（post_id 解析透传）", func(t *testing.T) {
		pc := &fakePostCache{}
		u := newBFFForPostTestWithCache(newUser(), &fakeMediaClient{}, &fakePostClient{viewPost: newPost()}, pc)

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "42"})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, []int64{42}, pc.gotIncr)
	})

	t.Run("作者自访：不计", func(t *testing.T) {
		pc := &fakePostCache{}
		u := newBFFForPostTestWithCache(newUser(), &fakeMediaClient{}, &fakePostClient{viewPost: newPost()}, pc)

		_, err := u.GetPost(ctxWithUID("u2"), &bffv1.GetPostRequest{PostId: "42"})
		require.NoError(t, err)
		assert.Empty(t, pc.gotIncr, "作者访问自己的帖子不计浏览")
	})

	t.Run("Redis 故障降级：仅 Warn，详情正常返回", func(t *testing.T) {
		pc := &fakePostCache{incrErr: errors.New("redis down")}
		u := newBFFForPostTestWithCache(newUser(), &fakeMediaClient{}, &fakePostClient{viewPost: newPost()}, pc)

		resp, err := u.GetPost(ctxWithUID("u1"), &bffv1.GetPostRequest{PostId: "42"})
		require.NoError(t, err)
		assert.False(t, resp.GetFail(), "埋点失败不得打挂详情页")
		assert.Equal(t, []int64{42}, pc.gotIncr)
	})
}
