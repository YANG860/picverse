package v1

import (
	"context"
	"errors"
	"testing"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	postv1 "rednote/gen/go/post/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 评论透传 fake：uid 由 BFF 会话注入，共享错误注入（commentFail/commentRPCErr）

func (f *fakePostClient) CreateComment(_ context.Context, req *connect.Request[postv1.CreateCommentRequest]) (*connect.Response[postv1.CreateCommentResponse], error) {
	f.gotCreateComment = req.Msg
	if f.commentRPCErr != nil {
		return nil, f.commentRPCErr
	}
	return connect.NewResponse(&postv1.CreateCommentResponse{
		Fail: f.commentFail != nil, Err: f.commentFail, Comment: f.commentResp, CommentCount: f.commentCount,
	}), nil
}

func (f *fakePostClient) ReplyComment(_ context.Context, req *connect.Request[postv1.ReplyCommentRequest]) (*connect.Response[postv1.ReplyCommentResponse], error) {
	f.gotReplyComment = req.Msg
	if f.commentRPCErr != nil {
		return nil, f.commentRPCErr
	}
	return connect.NewResponse(&postv1.ReplyCommentResponse{
		Fail: f.commentFail != nil, Err: f.commentFail, Comment: f.commentResp, CommentCount: f.commentCount,
	}), nil
}

func (f *fakePostClient) DeleteComment(_ context.Context, req *connect.Request[postv1.DeleteCommentRequest]) (*connect.Response[postv1.DeleteCommentResponse], error) {
	f.gotDeleteComment = req.Msg
	if f.commentRPCErr != nil {
		return nil, f.commentRPCErr
	}
	return connect.NewResponse(&postv1.DeleteCommentResponse{
		Fail: f.commentFail != nil, Err: f.commentFail, CommentCount: f.commentCount, Deleted: true,
	}), nil
}

func (f *fakePostClient) ListComments(_ context.Context, req *connect.Request[postv1.ListCommentsRequest]) (*connect.Response[postv1.ListCommentsResponse], error) {
	f.gotListComments = req.Msg
	if f.commentRPCErr != nil {
		return nil, f.commentRPCErr
	}
	return connect.NewResponse(&postv1.ListCommentsResponse{
		Fail: f.commentFail != nil, Err: f.commentFail, Comments: f.commentList, Cursor: f.commentCursor,
	}), nil
}

func (f *fakePostClient) ListReplies(_ context.Context, req *connect.Request[postv1.ListRepliesRequest]) (*connect.Response[postv1.ListRepliesResponse], error) {
	f.gotListReplies = req.Msg
	if f.commentRPCErr != nil {
		return nil, f.commentRPCErr
	}
	return connect.NewResponse(&postv1.ListRepliesResponse{
		Fail: f.commentFail != nil, Err: f.commentFail, Replies: f.commentList, Cursor: f.commentCursor,
	}), nil
}

// commentFixture 构造下游评论（评论者 u2，被回复者 u1，挂 u1 的楼层 7001）
func commentFixture() *postv1.Comment {
	return &postv1.Comment{
		CommentId:     "7002",
		PostId:        "2001",
		RootId:        "7001",
		ParentId:      "7001",
		UserId:        "u2",
		ReplyToUserId: "u1",
		Content:       "同问",
		CreatedAt:     "1000",
	}
}

// TestBFFComments 评论聚合：uid 会话注入 + 用户简视图批量聚合 + 错误映射 + 游标透传
func TestBFFComments(t *testing.T) {
	t.Run("AddComment：uid 来自会话，评论视图 + 计数回传", func(t *testing.T) {
		post := &fakePostClient{commentResp: commentFixture(), commentCount: 4}
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
			{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "甲"}},
			{Uid: "u2", Brief: &userv1.UserBrief{Uid: "u2", Name: "乙"}},
		}}
		bff := newBFFForPostTest(user, &fakeMediaClient{}, post)

		resp, err := bff.AddComment(ctxWithUID("u1"), &bffv1.AddCommentRequest{PostId: "2001", Content: "同问"})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		require.NotNil(t, post.gotCreateComment)
		assert.Equal(t, "u1", post.gotCreateComment.GetUid(), "uid 来自会话注入下游")
		assert.Equal(t, "2001", post.gotCreateComment.GetPostId())
		assert.Equal(t, "同问", post.gotCreateComment.GetContent())

		assert.Equal(t, int64(4), resp.GetCommentCount())
		require.NotNil(t, resp.GetComment())
		assert.Equal(t, "7002", resp.GetComment().GetCommentId())
		assert.Equal(t, "7001", resp.GetComment().GetRootId())
		assert.Equal(t, "u2", resp.GetComment().GetUser().GetUid(), "评论者聚合为 UserBrief")
		assert.Equal(t, "u1", resp.GetComment().GetReplyToUser().GetUid(), "被回复者聚合为 UserBrief")
	})

	t.Run("ReplyComment：parent 透传 + reply_to 聚合（简视图一次批量）", func(t *testing.T) {
		post := &fakePostClient{commentResp: commentFixture(), commentCount: 5}
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
			{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "甲"}},
			{Uid: "u2", Brief: &userv1.UserBrief{Uid: "u2", Name: "乙"}},
		}}
		bff := newBFFForPostTest(user, &fakeMediaClient{}, post)

		resp, err := bff.ReplyComment(ctxWithUID("u2"), &bffv1.ReplyCommentRequest{
			PostId: "2001", ParentId: "7001", Content: "同问",
		})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		require.NotNil(t, post.gotReplyComment)
		assert.Equal(t, "u2", post.gotReplyComment.GetUid())
		assert.Equal(t, "7001", post.gotReplyComment.GetParentId())
		assert.Len(t, user.gotBriefReqs, 1, "评论者/被回复者去重一次批量取简视图")
		assert.Equal(t, "u1", resp.GetComment().GetReplyToUser().GetUid())
	})

	t.Run("DeleteComment：uid 注入 + 计数/态位透传", func(t *testing.T) {
		post := &fakePostClient{commentCount: 3}
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)

		resp, err := bff.DeleteComment(ctxWithUID("u2"), &bffv1.DeleteCommentRequest{CommentId: "7002"})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, "u2", post.gotDeleteComment.GetUid())
		assert.Equal(t, int64(3), resp.GetCommentCount())
		assert.True(t, resp.GetDeleted())
	})

	t.Run("GetComments：列表聚合 + 游标双向透传", func(t *testing.T) {
		post := &fakePostClient{
			commentList: []*postv1.Comment{
				commentFixture(),
				{CommentId: "7003", PostId: "2001", RootId: "7003", UserId: "u1", Content: "沙发", CreatedAt: "2000", ReplyCount: 2},
			},
			commentCursor: &commonv1.Cursor{Str: "900", Limit: 20},
		}
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
			{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "甲"}},
		}}
		bff := newBFFForPostTest(user, &fakeMediaClient{}, post)

		reqCursor := &commonv1.Cursor{Str: "1500", Limit: 20}
		resp, err := bff.GetComments(ctxWithUID("u9"), &bffv1.GetCommentsRequest{PostId: "2001", Cursor: reqCursor})
		require.NoError(t, err)
		require.False(t, resp.GetFail())

		assert.Equal(t, reqCursor, post.gotListComments.GetCursor(), "请求游标透传下游")
		assert.Equal(t, post.commentCursor, resp.GetCursor(), "下游游标原样返回")
		require.Len(t, resp.GetComments(), 2)
		assert.Equal(t, "7002", resp.GetComments()[0].GetCommentId())
		assert.Nil(t, resp.GetComments()[0].GetUser(), "用户缺失降级置空不打挂列表")
		assert.Equal(t, "u1", resp.GetComments()[0].GetReplyToUser().GetUid(), "被回复者正常聚合")
		assert.Equal(t, "u1", resp.GetComments()[1].GetUser().GetUid())
		assert.Equal(t, int64(2), resp.GetComments()[1].GetReplyCount())
	})

	t.Run("GetReplies：透传 + 聚合", func(t *testing.T) {
		post := &fakePostClient{commentList: []*postv1.Comment{commentFixture()}}
		user := &fakeUserClient{briefResults: []*userv1.UserBriefResult{
			{Uid: "u1", Brief: &userv1.UserBrief{Uid: "u1", Name: "甲"}},
			{Uid: "u2", Brief: &userv1.UserBrief{Uid: "u2", Name: "乙"}},
		}}
		bff := newBFFForPostTest(user, &fakeMediaClient{}, post)

		resp, err := bff.GetReplies(ctxWithUID("u9"), &bffv1.GetRepliesRequest{RootId: "7001"})
		require.NoError(t, err)
		require.False(t, resp.GetFail())
		assert.Equal(t, "7001", post.gotListReplies.GetRootId())
		require.Len(t, resp.GetReplies(), 1)
		assert.Equal(t, "u1", resp.GetReplies()[0].GetReplyToUser().GetUid())
	})

	t.Run("错误映射：业务 fail 透传 / 连接层 InternalError / 无会话 Unauthorized", func(t *testing.T) {
		// 业务 fail 透传
		post := &fakePostClient{commentFail: errmsg.NotFound}
		bff := newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)
		resp, err := bff.AddComment(ctxWithUID("u1"), &bffv1.AddCommentRequest{PostId: "2001", Content: "x"})
		require.NoError(t, err)
		assert.True(t, resp.GetFail())
		assert.Equal(t, errmsg.NotFound, resp.GetErr())

		// 连接层错误 → InternalError（不透传内部细节）
		post = &fakePostClient{commentRPCErr: errors.New("conn refused")}
		bff = newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)
		listResp, err := bff.GetComments(ctxWithUID("u1"), &bffv1.GetCommentsRequest{PostId: "2001"})
		require.NoError(t, err)
		assert.True(t, listResp.GetFail())
		assert.Equal(t, errmsg.InternalError, listResp.GetErr())

		// 无会话 → Unauthorized（不透传下游，连调用都不发生）
		post = &fakePostClient{}
		bff = newBFFForPostTest(&fakeUserClient{}, &fakeMediaClient{}, post)
		delResp, err := bff.DeleteComment(context.Background(), &bffv1.DeleteCommentRequest{CommentId: "7002"})
		require.NoError(t, err)
		assert.True(t, delResp.GetFail())
		assert.Equal(t, errmsg.Unauthorized, delResp.GetErr())
		assert.Nil(t, post.gotDeleteComment, "未鉴权不透传下游")
	})
}
