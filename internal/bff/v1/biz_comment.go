package v1

import (
	"context"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	postv1 "rednote/gen/go/post/v1"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
)

// ==================== 评论 ====================

// commentViews 批量组装面客评论视图：评论者与被回复者去重一次批量取简视图（authorViews），
// 逐项缺失降级置空（列表可用性优先，不打挂整页）
func (u *BFFUsecaseImpl) commentViews(ctx context.Context, comments []*postv1.Comment) ([]*bffv1.CommentView, *commonv1.Error) {
	if len(comments) == 0 {
		return []*bffv1.CommentView{}, nil
	}

	uids := make([]string, 0, len(comments)*2)
	for _, c := range comments {
		uids = append(uids, c.GetUserId(), c.GetReplyToUserId())
	}
	users, errCode := u.authorViews(ctx, uids)
	if errCode != nil {
		return nil, errCode
	}

	views := make([]*bffv1.CommentView, len(comments))
	for i, c := range comments {
		user := users[c.GetUserId()]
		if user == nil {
			u.log.Warn("comment user missing，降级置空", "comment_id", c.GetCommentId(), "uid", c.GetUserId())
		}
		views[i] = &bffv1.CommentView{
			CommentId:   c.GetCommentId(),
			RootId:      c.GetRootId(),
			ParentId:    c.GetParentId(),
			User:        user,
			ReplyToUser: users[c.GetReplyToUserId()],
			Content:     c.GetContent(),
			ReplyCount:  c.GetReplyCount(),
			CreatedAt:   c.GetCreatedAt(),
		}
	}
	return views, nil
}

// AddComment 发表一级评论（uid 来自会话；同步校验+审核由 post 服务在请求内完成）；
// 响应回显评论视图 + 帖子最新 comment_count
func (u *BFFUsecaseImpl) AddComment(ctx context.Context, req *bffv1.AddCommentRequest) (*bffv1.AddCommentResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.AddCommentResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.CreateComment(ctx, connect.NewRequest(&postv1.CreateCommentRequest{
		Uid: uid, PostId: req.GetPostId(), Content: req.GetContent(),
	}))
	if err != nil {
		u.log.Error("post.CreateComment failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.AddCommentResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.AddCommentResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	views, mapErr := u.commentViews(ctx, []*postv1.Comment{resp.Msg.GetComment()})
	if mapErr != nil {
		return &bffv1.AddCommentResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.AddCommentResponse{Comment: views[0], CommentCount: resp.Msg.GetCommentCount()}, nil
}

// ReplyComment 回复（楼中楼：parent 可为一级评论或楼内回复；uid 来自会话）
func (u *BFFUsecaseImpl) ReplyComment(ctx context.Context, req *bffv1.ReplyCommentRequest) (*bffv1.ReplyCommentResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.ReplyCommentResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.ReplyComment(ctx, connect.NewRequest(&postv1.ReplyCommentRequest{
		Uid: uid, PostId: req.GetPostId(), ParentId: req.GetParentId(), Content: req.GetContent(),
	}))
	if err != nil {
		u.log.Error("post.ReplyComment failed", "uid", uid, "post_id", req.GetPostId(), "parent_id", req.GetParentId(), "error", err)
		return &bffv1.ReplyCommentResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.ReplyCommentResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	views, mapErr := u.commentViews(ctx, []*postv1.Comment{resp.Msg.GetComment()})
	if mapErr != nil {
		return &bffv1.ReplyCommentResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.ReplyCommentResponse{Comment: views[0], CommentCount: resp.Msg.GetCommentCount()}, nil
}

// DeleteComment 删除评论（软删；删一级评论级联软删整楼，幂等；uid 来自会话，权限由下游裁决）
func (u *BFFUsecaseImpl) DeleteComment(ctx context.Context, req *bffv1.DeleteCommentRequest) (*bffv1.DeleteCommentResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.DeleteCommentResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.DeleteComment(ctx, connect.NewRequest(&postv1.DeleteCommentRequest{
		Uid: uid, CommentId: req.GetCommentId(),
	}))
	if err != nil {
		u.log.Error("post.DeleteComment failed", "uid", uid, "comment_id", req.GetCommentId(), "error", err)
		return &bffv1.DeleteCommentResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.DeleteCommentResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	return &bffv1.DeleteCommentResponse{CommentCount: resp.Msg.GetCommentCount(), Deleted: resp.Msg.GetDeleted()}, nil
}

// GetComments 帖子一级评论分页（created_at DESC 游标透传；空页 = 空列表 + 空游标）
func (u *BFFUsecaseImpl) GetComments(ctx context.Context, req *bffv1.GetCommentsRequest) (*bffv1.GetCommentsResponse, error) {
	if _, errCode := u.sessionUID(ctx); errCode != nil {
		return &bffv1.GetCommentsResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.ListComments(ctx, connect.NewRequest(&postv1.ListCommentsRequest{
		PostId: req.GetPostId(), Cursor: req.GetCursor(),
	}))
	if err != nil {
		u.log.Error("post.ListComments failed", "post_id", req.GetPostId(), "error", err)
		return &bffv1.GetCommentsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetCommentsResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	views, mapErr := u.commentViews(ctx, resp.Msg.GetComments())
	if mapErr != nil {
		return &bffv1.GetCommentsResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetCommentsResponse{Comments: views, Cursor: resp.Msg.GetCursor()}, nil
}

// GetReplies 楼内回复分页（root_id = 楼层 id；平铺含回复的回复，游标透传）
func (u *BFFUsecaseImpl) GetReplies(ctx context.Context, req *bffv1.GetRepliesRequest) (*bffv1.GetRepliesResponse, error) {
	if _, errCode := u.sessionUID(ctx); errCode != nil {
		return &bffv1.GetRepliesResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.ListReplies(ctx, connect.NewRequest(&postv1.ListRepliesRequest{
		RootId: req.GetRootId(), Cursor: req.GetCursor(),
	}))
	if err != nil {
		u.log.Error("post.ListReplies failed", "root_id", req.GetRootId(), "error", err)
		return &bffv1.GetRepliesResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetRepliesResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	views, mapErr := u.commentViews(ctx, resp.Msg.GetReplies())
	if mapErr != nil {
		return &bffv1.GetRepliesResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetRepliesResponse{Replies: views, Cursor: resp.Msg.GetCursor()}, nil
}
