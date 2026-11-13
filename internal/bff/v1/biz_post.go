package v1

import (
	"context"
	"strconv"
	"time"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
)

// ==================== 帖子（两步发布：保存草稿 → 发布） ====================

// mediaTTLFor 面客 URL TTL 分级：PUBLISHED 公开 7d 上限，草稿/审核中等私有 24h。
func mediaTTLFor(status postv1.PostStatus) time.Duration {
	if status == postv1.PostStatus_POST_STATUS_PUBLISHED {
		return publicMediaTTL
	}
	return privateMediaTTL
}

// postView 把下游 Post 降维为面客 PostView（草稿写入回显；作者即会话用户，不聚合作者/关系），
// medias 经 signMediaViews 按状态分级签发。
func (u *BFFUsecaseImpl) postView(ctx context.Context, p *postv1.Post) (*bffv1.PostView, *commonv1.Error) {
	views, errCode := u.signMediaViews(ctx, mediaTTLFor(p.GetStatus()), p.GetMedias(), false)
	if errCode != nil {
		return nil, errCode
	}
	return &bffv1.PostView{
		PostId:    p.GetPostId(),
		Title:     p.GetTitle(),
		FullText:  p.GetFullText(),
		Medias:    views,
		Metadata:  p.GetMetadata(),
		Stat:      p.GetPostStat(),
		CreatedAt: p.GetCreatedAt(),
		UpdatedAt: p.GetUpdatedAt(),
		Status:    p.GetStatus(),
	}, nil
}

// CreateDraft 创建草稿：作者即会话用户，媒体须为其素材库资产（批量归属校验 + 挂载 +1）。
// 下游失败回滚挂载计数（发卸载消息）；回显 PostView（草稿媒体私有 TTL）。
func (u *BFFUsecaseImpl) CreateDraft(ctx context.Context, req *bffv1.CreateDraftRequest) (*bffv1.CreateDraftResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.CreateDraftResponse{Fail: true, Err: errCode}, nil
	}

	files, refErr := u.GetAndRefMediaFiles(ctx, req.GetMedias(), uid)
	if refErr != nil {
		return &bffv1.CreateDraftResponse{Fail: true, Err: refErr}, nil
	}

	resp, err := u.Repo.Post.CreateDraft(ctx, connect.NewRequest(&postv1.CreateDraftRequest{
		Uid:      uid,
		Title:    req.GetTitle(),
		FullText: req.GetFullText(),
		Medias:   files,
		Metadata: req.GetMetadata(),
	}))
	if err != nil {
		u.log.Error("post.CreateDraft failed", "uid", uid, "error", err)
		u.rollbackRefs(ctx, req.GetMedias(), uid)
		return &bffv1.CreateDraftResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		u.rollbackRefs(ctx, req.GetMedias(), uid)
		return &bffv1.CreateDraftResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	view, mapErr := u.postView(ctx, resp.Msg.GetDraft())
	if mapErr != nil {
		return &bffv1.CreateDraftResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.CreateDraftResponse{Post: view}, nil
}

// UpdateDraft 更新草稿文本字段（部分更新：nil 不修改，仅 DRAFT 可更新），透传下游并回显 PostView。
func (u *BFFUsecaseImpl) UpdateDraft(ctx context.Context, req *bffv1.UpdateDraftRequest) (*bffv1.UpdateDraftResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.UpdateDraftResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.UpdateDraft(ctx, connect.NewRequest(&postv1.UpdateDraftRequest{
		Uid:      uid,
		PostId:   req.GetPostId(),
		Title:    req.GetTitle(),
		FullText: req.GetFullText(),
		Metadata: req.GetMetadata(),
	}))
	if err != nil {
		u.log.Error("post.UpdateDraft failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.UpdateDraftResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.UpdateDraftResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	view, mapErr := u.postView(ctx, resp.Msg.GetPost())
	if mapErr != nil {
		return &bffv1.UpdateDraftResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.UpdateDraftResponse{Post: view}, nil
}

// UpdateDraftMedias 更新草稿媒体（全量替换）：先挂新（+1 同步）→ 下游替换 → 旧资产发卸载消息（-1，MQ 异步）。
// 下游失败回滚新挂载；卸载消息发送失败只记 ERROR（重试会错卸新资产），待闲置回收兜底。
// 旧资产 ID 是下游中间产物，不外泄给客户端。
func (u *BFFUsecaseImpl) UpdateDraftMedias(ctx context.Context, req *bffv1.UpdateDraftMediasRequest) (*bffv1.UpdateDraftMediasResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.UpdateDraftMediasResponse{Fail: true, Err: errCode}, nil
	}

	files, refErr := u.GetAndRefMediaFiles(ctx, req.GetMedias(), uid)
	if refErr != nil {
		return &bffv1.UpdateDraftMediasResponse{Fail: true, Err: refErr}, nil
	}

	resp, err := u.Repo.Post.UpdateDraftMedias(ctx, connect.NewRequest(&postv1.UpdateDraftMediasRequest{
		Uid:    uid,
		PostId: req.GetPostId(),
		Medias: files,
	}))
	if err != nil {
		u.log.Error("post.UpdateDraftMedias failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		u.rollbackRefs(ctx, req.GetMedias(), uid)
		return &bffv1.UpdateDraftMediasResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		u.rollbackRefs(ctx, req.GetMedias(), uid)
		return &bffv1.UpdateDraftMediasResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	// 卸载旧媒体槽位（MQ 异步，最终一致）
	for _, oldID := range resp.Msg.GetOldAssetIds() {
		if derefErr := u.DeRefMediaFile(ctx, oldID); derefErr != nil {
			u.log.Error("deref old media failed", "uid", uid, "old_asset_id", oldID, "err_code", derefErr.GetCode())
		}
	}

	view, mapErr := u.postView(ctx, resp.Msg.GetPost())
	if mapErr != nil {
		return &bffv1.UpdateDraftMediasResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.UpdateDraftMediasResponse{Post: view}, nil
}

// PublishDraft 发布草稿（DRAFT → AUDITING，审核通过后对外可见），uid 会话注入透传。
func (u *BFFUsecaseImpl) PublishDraft(ctx context.Context, req *bffv1.PublishDraftRequest) (*bffv1.PublishDraftResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.PublishDraftResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.PublishDraft(ctx, connect.NewRequest(&postv1.PublishDraftRequest{Uid: uid, PostId: req.GetPostId()}))
	if err != nil {
		u.log.Error("post.PublishDraft failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.PublishDraftResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.PublishDraftResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}
	return &bffv1.PublishDraftResponse{}, nil
}

// ==================== 帖子查询 ====================

// postDetail 组装面客 PostDetail：medias 按状态分级签发，作者/关系由调用方聚合后传入。
func (u *BFFUsecaseImpl) postDetail(ctx context.Context, p *postv1.Post, author *bffv1.UserBrief, relation *bffv1.Relation) (*bffv1.PostDetail, *commonv1.Error) {
	views, errCode := u.signMediaViews(ctx, mediaTTLFor(p.GetStatus()), p.GetMedias(), false)
	if errCode != nil {
		return nil, errCode
	}
	return &bffv1.PostDetail{
		PostId:    p.GetPostId(),
		Title:     p.GetTitle(),
		FullText:  p.GetFullText(),
		Medias:    views,
		Metadata:  p.GetMetadata(),
		Author:    author,
		Stat:      p.GetPostStat(),
		Relation:  relation,
		CreatedAt: p.GetCreatedAt(),
		UpdatedAt: p.GetUpdatedAt(),
		Status:    p.GetStatus(),
	}, nil
}

// authorViews 批量取作者面客简视图（头像公开 TTL），按 uid 去重一次批量调用；
// 逐项失败记 ERROR 跳过（列表可用性优先，缺失作者的卡片 author 置空，不打挂整页）。
func (u *BFFUsecaseImpl) authorViews(ctx context.Context, uids []string) (map[string]*bffv1.UserBrief, *commonv1.Error) {
	seen := make(map[string]struct{}, len(uids))
	dedup := make([]string, 0, len(uids))
	for _, id := range uids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		dedup = append(dedup, id)
	}

	resp, err := u.Repo.User.GetUserBrief(ctx, connect.NewRequest(&userv1.GetUserBriefRequest{Uids: dedup}))
	if err != nil {
		u.log.Error("user.GetUserBrief failed", "uids", dedup, "error", err)
		return nil, errmsg.InternalError
	}

	ok := make(map[string]*userv1.UserBrief, len(dedup))
	okUIDs := make([]string, 0, len(dedup))
	for _, r := range resp.Msg.GetResults() {
		if r.GetFail() {
			u.log.Warn("user.GetUserBrief item failed，降级跳过", "uid", r.GetUid(), "err_code", r.GetErr().GetCode())
			continue
		}
		ok[r.GetUid()] = r.GetBrief()
		okUIDs = append(okUIDs, r.GetUid())
	}
	if len(okUIDs) == 0 {
		return map[string]*bffv1.UserBrief{}, nil
	}

	// 头像一次批量签发（公开 TTL）后按 uid 回填
	briefs := make([]*userv1.UserBrief, 0, len(okUIDs))
	for _, id := range okUIDs {
		briefs = append(briefs, ok[id])
	}
	views, errCode := u.userBriefsView(ctx, briefs)
	if errCode != nil {
		return nil, errCode
	}
	authors := make(map[string]*bffv1.UserBrief, len(okUIDs))
	for i, id := range okUIDs {
		authors[id] = views[i]
	}
	return authors, nil
}

// postCards 批量组装 PostCard：covers 按状态分组批量签发（公开/私有各一次内网调用），
// 状态直接取自下游 PostBrief（含 status，无需二次查询）；作者视图由调用方预取
// （authors 缺失的卡片 author 置空，记 ERROR 不打挂整页）。
func (u *BFFUsecaseImpl) postCards(ctx context.Context, briefs []*postv1.PostBrief, authors map[string]*bffv1.UserBrief) ([]*bffv1.PostCard, *commonv1.Error) {
	cards := make([]*bffv1.PostCard, len(briefs))
	if len(briefs) == 0 {
		return cards, nil
	}

	views := make([]*bffv1.MediaView, len(briefs))
	var pubIdx, privIdx []int
	for i := range briefs {
		if briefs[i].GetStatus() == postv1.PostStatus_POST_STATUS_PUBLISHED {
			pubIdx = append(pubIdx, i)
		} else {
			privIdx = append(privIdx, i)
		}
	}
	signGroup := func(idxs []int, ttl time.Duration) *commonv1.Error {
		if len(idxs) == 0 {
			return nil
		}
		files := make([]*mediav1.MediaFile, len(idxs))
		for j, i := range idxs {
			files[j] = briefs[i].GetCover()
		}
		groupViews, errCode := u.signMediaViews(ctx, ttl, files, true) // 卡片封面弱字段：脏资产降级不拖垮整页
		if errCode != nil {
			return errCode
		}
		for j, i := range idxs {
			views[i] = groupViews[j]
		}
		return nil
	}
	if errCode := signGroup(pubIdx, publicMediaTTL); errCode != nil {
		return nil, errCode
	}
	if errCode := signGroup(privIdx, privateMediaTTL); errCode != nil {
		return nil, errCode
	}

	for i, b := range briefs {
		author := authors[b.GetAuthorId()]
		if author == nil {
			u.log.Warn("post card author missing，降级置空", "post_id", b.GetPostId(), "author_id", b.GetAuthorId())
		}
		cards[i] = &bffv1.PostCard{
			PostId:    b.GetPostId(),
			Title:     b.GetTitle(),
			Cover:     views[i],
			Author:    author,
			Stat:      b.GetStat(),
			CreatedAt: b.GetCreatedAt(),
			Status:    b.GetStatus(),
		}
	}
	return cards, nil
}

// GetPost 帖子详情：可见性/软删/封禁由下游 ViewPost 裁决（非发布状态仅作者可见，Banned/NotFound 透传），
// 并行聚合作者简视图与会话用户对作者的关注关系（无关系记录按未关注降级，不打挂详情；
// 其余关系错误透传）。媒体按状态分级签发 TTL。
func (u *BFFUsecaseImpl) GetPost(ctx context.Context, req *bffv1.GetPostRequest) (*bffv1.GetPostResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetPostResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.ViewPost(ctx, connect.NewRequest(&postv1.ViewPostRequest{ViewerId: uid, PostId: req.GetPostId()}))
	if err != nil {
		u.log.Error("post.ViewPost failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.GetPostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetPostResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}
	p := resp.Msg.GetPost()

	var briefResp *connect.Response[userv1.GetUserBriefResponse]
	var relationResp *connect.Response[userv1.GetFollowingRelationResponse]
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		briefResp, err = u.Repo.User.GetUserBrief(gctx, connect.NewRequest(&userv1.GetUserBriefRequest{Uids: []string{p.GetAuthorId()}}))
		return err
	})
	g.Go(func() error {
		var err error
		relationResp, err = u.Repo.User.GetFollowingRelation(gctx, connect.NewRequest(&userv1.GetFollowingRelationRequest{Uid: uid, TargetUid: p.GetAuthorId()}))
		return err
	})
	if err := g.Wait(); err != nil {
		u.log.Error("user.GetUserBrief/GetFollowingRelation failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.GetPostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	// 关系降级同 GetUserProfile：NotFound 按未关注/未被关注展示
	relation := &bffv1.Relation{}
	if !relationResp.Msg.GetFail() {
		relation.IsFollowing = relationResp.Msg.GetIsFollowing()
		relation.IsFollower = relationResp.Msg.GetIsFollower()
	} else if relationResp.Msg.GetErr().GetCode() != errmsg.NotFound.Code {
		u.log.Error("user.GetFollowingRelation failed", "uid", uid, "post_id", req.GetPostId(),
			"err_code", relationResp.Msg.GetErr().GetCode())
		return &bffv1.GetPostResponse{Fail: true, Err: relationResp.Msg.GetErr()}, nil
	}

	// 作者是详情必需项：批量结果逐项定位，失败透传
	var authorBrief *userv1.UserBrief
	for _, r := range briefResp.Msg.GetResults() {
		if r.GetFail() {
			return &bffv1.GetPostResponse{Fail: true, Err: r.GetErr()}, nil
		}
		authorBrief = r.GetBrief()
	}
	if authorBrief == nil {
		u.log.Error("user.GetUserBrief missing author", "post_id", req.GetPostId(), "author_id", p.GetAuthorId())
		return &bffv1.GetPostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	views, mapErr := u.userBriefsView(ctx, []*userv1.UserBrief{authorBrief})
	if mapErr != nil {
		return &bffv1.GetPostResponse{Fail: true, Err: mapErr}, nil
	}

	detail, mapErr := u.postDetail(ctx, p, views[0], relation)
	if mapErr != nil {
		return &bffv1.GetPostResponse{Fail: true, Err: mapErr}, nil
	}

	// 浏览计数埋点（PV 近似）：他人打开详情计数 +1，作者自访不计；
	// best-effort，失败仅 Warn 不打挂详情页；RedisPoller 定时批量写回 posts.view_count
	if uid != p.GetAuthorId() {
		if postID, err := strconv.ParseInt(p.GetPostId(), 10, 64); err != nil {
			u.log.Warn("skip view count: invalid post id", "uid", uid, "post_id", p.GetPostId())
		} else if err := u.PostCache.IncrPostViewCount(ctx, postID); err != nil {
			u.log.Warn("incr view count failed (best-effort)", "uid", uid, "post_id", postID, "error", err)
		}
	}

	return &bffv1.GetPostResponse{Post: detail}, nil
}

// GetMyPosts 我的帖子列表（含草稿/审核中）：ViewPostByAuthor（viewer=author）游标透传，
// 状态取自下游 PostBrief（含 status），卡片封面按状态分级签发 TTL；作者即会话用户，仅取一次。
func (u *BFFUsecaseImpl) GetMyPosts(ctx context.Context, req *bffv1.GetMyPostsRequest) (*bffv1.GetMyPostsResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetMyPostsResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.ViewPostByAuthor(ctx, connect.NewRequest(&postv1.ViewPostByAuthorRequest{ViewerId: uid, AuthorId: uid, Cursor: req.GetCursor()}))
	if err != nil {
		u.log.Error("post.ViewPostByAuthor failed", "uid", uid, "error", err)
		return &bffv1.GetMyPostsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetMyPostsResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	authors, authErr := u.authorViews(ctx, []string{uid})
	if authErr != nil {
		return &bffv1.GetMyPostsResponse{Fail: true, Err: authErr}, nil
	}

	cards, mapErr := u.postCards(ctx, resp.Msg.GetPosts(), authors)
	if mapErr != nil {
		return &bffv1.GetMyPostsResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetMyPostsResponse{Posts: cards, Cursor: resp.Msg.GetCursor()}, nil
}

// GetUserPosts 他人主页的帖子列表（仅已发布，下游过滤）：游标透传，
// 卡片状态/封面 TTL 统一取自下游 PostBrief（他人视角恒 PUBLISHED），作者为目标用户。
func (u *BFFUsecaseImpl) GetUserPosts(ctx context.Context, req *bffv1.GetUserPostsRequest) (*bffv1.GetUserPostsResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetUserPostsResponse{Fail: true, Err: errCode}, nil
	}
	target := req.GetUid()

	// TODO: 传自己时候返回他人看自己的主页（所有公开作品）
	if target == uid {
		return &bffv1.GetUserPostsResponse{}, nil
	}

	resp, err := u.Repo.Post.ViewPostByAuthor(ctx, connect.NewRequest(&postv1.ViewPostByAuthorRequest{ViewerId: uid, AuthorId: target, Cursor: req.GetCursor()}))
	if err != nil {
		u.log.Error("post.ViewPostByAuthor failed", "uid", uid, "target", target, "error", err)
		return &bffv1.GetUserPostsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetUserPostsResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	authors, authErr := u.authorViews(ctx, []string{target})
	if authErr != nil {
		return &bffv1.GetUserPostsResponse{Fail: true, Err: authErr}, nil
	}

	cards, mapErr := u.postCards(ctx, resp.Msg.GetPosts(), authors)
	if mapErr != nil {
		return &bffv1.GetUserPostsResponse{Fail: true, Err: mapErr}, nil
	}
	return &bffv1.GetUserPostsResponse{Posts: cards, Cursor: resp.Msg.GetCursor()}, nil
}

// DeletePost 删除帖子（软删），uid 会话注入透传。
// 资产计数回收由删帖消息链路完成：软删事务内发 DeletePost 消息（outbox）→ consumer 物理回收时
// 按被删媒体关联逐槽位写 deref 消息递减（见 doc/internal/bff/bff.md 待定项 3，已闭环）。
func (u *BFFUsecaseImpl) DeletePost(ctx context.Context, req *bffv1.DeletePostRequest) (*bffv1.DeletePostResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.DeletePostResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.DeletePost(ctx, connect.NewRequest(&postv1.DeletePostRequest{Uid: uid, PostId: req.GetPostId()}))
	if err != nil {
		u.log.Error("post.DeletePost failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.DeletePostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.DeletePostResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}
	return &bffv1.DeletePostResponse{}, nil
}

// ==================== 互动（点赞/收藏） ====================
// 口径：薄透传（可见性由 post 下游裁决）；
// 响应回传最新计数 + 状态位，前端据此同步本地判态集合

// LikePost 点赞（uid 来自会话；幂等透传）
func (u *BFFUsecaseImpl) LikePost(ctx context.Context, req *bffv1.LikePostRequest) (*bffv1.LikePostResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.LikePostResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.LikePost(ctx, connect.NewRequest(&postv1.LikePostRequest{Uid: uid, PostId: req.GetPostId()}))
	if err != nil {
		u.log.Error("post.LikePost failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.LikePostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.LikePostResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	return &bffv1.LikePostResponse{LikeCount: resp.Msg.GetLikeCount(), Liked: resp.Msg.GetLiked()}, nil
}

// UnlikePost 取消点赞（幂等透传）
func (u *BFFUsecaseImpl) UnlikePost(ctx context.Context, req *bffv1.UnlikePostRequest) (*bffv1.UnlikePostResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.UnlikePostResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.UnlikePost(ctx, connect.NewRequest(&postv1.UnlikePostRequest{Uid: uid, PostId: req.GetPostId()}))
	if err != nil {
		u.log.Error("post.UnlikePost failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.UnlikePostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.UnlikePostResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	return &bffv1.UnlikePostResponse{LikeCount: resp.Msg.GetLikeCount(), Liked: resp.Msg.GetLiked()}, nil
}

// FavouritePost 收藏（幂等透传；容量超限业务码透传）
func (u *BFFUsecaseImpl) FavouritePost(ctx context.Context, req *bffv1.FavouritePostRequest) (*bffv1.FavouritePostResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.FavouritePostResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.FavouritePost(ctx, connect.NewRequest(&postv1.FavouritePostRequest{Uid: uid, PostId: req.GetPostId()}))
	if err != nil {
		u.log.Error("post.FavouritePost failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.FavouritePostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.FavouritePostResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	return &bffv1.FavouritePostResponse{FavouriteCount: resp.Msg.GetFavouriteCount(), Favourited: resp.Msg.GetFavourited()}, nil
}

// UnfavouritePost 取消收藏（幂等透传）
func (u *BFFUsecaseImpl) UnfavouritePost(ctx context.Context, req *bffv1.UnfavouritePostRequest) (*bffv1.UnfavouritePostResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.UnfavouritePostResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.UnfavouritePost(ctx, connect.NewRequest(&postv1.UnfavouritePostRequest{Uid: uid, PostId: req.GetPostId()}))
	if err != nil {
		u.log.Error("post.UnfavouritePost failed", "uid", uid, "post_id", req.GetPostId(), "error", err)
		return &bffv1.UnfavouritePostResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.UnfavouritePostResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	return &bffv1.UnfavouritePostResponse{FavouriteCount: resp.Msg.GetFavouriteCount(), Favourited: resp.Msg.GetFavourited()}, nil
}

// GetMyLikedPostIDs 我最近点赞的帖子 ID（uid 来自会话；游标透传，前端预取判态）
func (u *BFFUsecaseImpl) GetMyLikedPostIDs(ctx context.Context, req *bffv1.GetMyLikedPostIDsRequest) (*bffv1.GetMyLikedPostIDsResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetMyLikedPostIDsResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.ListLikedPostIDs(ctx, connect.NewRequest(&postv1.ListLikedPostIDsRequest{Uid: uid, Cursor: req.GetCursor()}))
	if err != nil {
		u.log.Error("post.ListLikedPostIDs failed", "uid", uid, "error", err)
		return &bffv1.GetMyLikedPostIDsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetMyLikedPostIDsResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	return &bffv1.GetMyLikedPostIDsResponse{PostIds: resp.Msg.GetPostIds(), Cursor: resp.Msg.GetCursor()}, nil
}

// GetMyFavouritePostIDs 我收藏的帖子 ID（同构；收藏夹容量上限内一页可拉全）
func (u *BFFUsecaseImpl) GetMyFavouritePostIDs(ctx context.Context, req *bffv1.GetMyFavouritePostIDsRequest) (*bffv1.GetMyFavouritePostIDsResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetMyFavouritePostIDsResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Post.ListFavouritePostIDs(ctx, connect.NewRequest(&postv1.ListFavouritePostIDsRequest{Uid: uid, Cursor: req.GetCursor()}))
	if err != nil {
		u.log.Error("post.ListFavouritePostIDs failed", "uid", uid, "error", err)
		return &bffv1.GetMyFavouritePostIDsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetMyFavouritePostIDsResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	return &bffv1.GetMyFavouritePostIDsResponse{PostIds: resp.Msg.GetPostIds(), Cursor: resp.Msg.GetCursor()}, nil
}
