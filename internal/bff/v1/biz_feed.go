package v1

import (
	"context"

	bffv1 "rednote/gen/go/bff/v1"
	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	postv1 "rednote/gen/go/post/v1"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
)

// ==================== 信息流 ====================

// postCardsByIDs post_ids → PostCard 列表（保持给定顺序）：批量面查 ViewPostsByIDs（
// 仅 PUBLISHED，可见性裁决收敛服务端）→ briefs → 作者视图 → 卡片。
// 面客组装的统一出口（搜索/热榜/推荐流/关注流共用）。
// 降级口径：单条 fail（不可见/非法）跳过（页内条数可能少于输入）；整页被过滤返回空列表（非错误）。
// ViewPostsByIDs 不保证序，此处按 ids 重排（discover 排序序即面客序）。
func (u *BFFUsecaseImpl) postCardsByIDs(ctx context.Context, ids []string) ([]*bffv1.PostCard, *commonv1.Error) {
	if len(ids) == 0 {
		return nil, nil
	}
	viewResp, err := u.Repo.Post.ViewPostsByIDs(ctx, connect.NewRequest(&postv1.ViewPostsByIDsRequest{PostIds: ids}))
	if err != nil {
		u.log.Error("post.ViewPostsByIDs failed", "post_ids", ids, "error", err)
		return nil, errmsg.InternalError
	}
	if viewResp.Msg.GetFail() {
		return nil, viewResp.Msg.GetErr()
	}

	visible := make(map[string]*postv1.PostBrief, len(ids))
	for _, r := range viewResp.Msg.GetResults() {
		if r.GetFail() || r.GetPost() == nil {
			u.log.Warn("feed post not visible，降级跳过", "post_id", r.GetPostId(), "err_code", r.GetErr().GetCode())
			continue
		}
		visible[r.GetPostId()] = r.GetPost()
	}

	briefs := make([]*postv1.PostBrief, 0, len(ids))
	uids := make([]string, 0, len(ids))
	for _, id := range ids { // 按 discover 排序序重排（面客序）
		b := visible[id]
		if b == nil {
			continue
		}
		briefs = append(briefs, b)
		uids = append(uids, b.GetAuthorId())
	}
	if len(briefs) == 0 {
		return nil, nil
	}

	authors, errCode := u.authorViews(ctx, uids)
	if errCode != nil {
		return nil, errCode
	}
	cards, errCode := u.postCards(ctx, briefs, authors)
	if errCode != nil {
		return nil, errCode
	}
	return cards, nil
}

// fetchFollowingIDs 拉取全量关注列表（GetFollowings 游标分页，封顶 MaxLimit——
// 关注流写扩散同量级；超封顶的关注被截断，超出部分不进关注流——proto 契约"封顶由 BFF 控制"）
func (u *BFFUsecaseImpl) fetchFollowingIDs(ctx context.Context, uid string) ([]string, *commonv1.Error) {
	var ids []string
	var cursor *commonv1.Cursor
	for {
		resp, err := u.Repo.User.GetFollowings(ctx, connect.NewRequest(&userv1.GetFollowingsRequest{Uid: uid, Cursor: cursor}))
		if err != nil {
			u.log.Error("user.GetFollowings failed", "uid", uid, "error", err)
			return nil, errmsg.InternalError
		}
		if resp.Msg.GetFail() {
			return nil, resp.Msg.GetErr()
		}
		for _, b := range resp.Msg.GetUserBriefs() {
			ids = append(ids, b.GetUid())
		}
		if len(ids) >= defaults.MaxLimit {
			return ids[:defaults.MaxLimit], nil
		}
		cursor = resp.Msg.GetCursor()
		if cursor == nil || len(resp.Msg.GetUserBriefs()) == 0 {
			return ids, nil
		}
	}
}

// GetFollowingFeed 关注流：拉全量关注列表（封顶）→ discover 推拉归并取 post_ids → 组装卡片。
// 游标透传 discover（面客翻页语义与内部一致：列表空且 cursor 空 = 到底）；
// BFF 回查过滤的删帖不吞游标（页内变少是近似口径，客户端继续翻页）
func (u *BFFUsecaseImpl) GetFollowingFeed(ctx context.Context, req *bffv1.GetFollowingFeedRequest) (*bffv1.GetFollowingFeedResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetFollowingFeedResponse{Fail: true, Err: errCode}, nil
	}
	following, errCode := u.fetchFollowingIDs(ctx, uid)
	if errCode != nil {
		return &bffv1.GetFollowingFeedResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Discover.GetFollowingFeed(ctx, connect.NewRequest(&discoverv1.GetFollowingFeedRequest{
		Uid: uid, FollowingIds: following, Cursor: req.GetCursor(),
	}))
	if err != nil {
		u.log.Error("discover.GetFollowingFeed failed", "uid", uid, "error", err)
		return &bffv1.GetFollowingFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetFollowingFeedResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	cards, errCode := u.postCardsByIDs(ctx, resp.Msg.GetPostIds())
	if errCode != nil {
		return &bffv1.GetFollowingFeedResponse{Fail: true, Err: errCode}, nil
	}
	return &bffv1.GetFollowingFeedResponse{Posts: cards, Cursor: resp.Msg.GetCursor()}, nil
}

// GetHotPosts 热榜：discover 热池全序翻页取 post_ids → 组装卡片（榜单语义：可重复看，不做去重）
func (u *BFFUsecaseImpl) GetHotPosts(ctx context.Context, req *bffv1.GetHotPostsRequest) (*bffv1.GetHotPostsResponse, error) {
	if _, errCode := u.sessionUID(ctx); errCode != nil {
		return &bffv1.GetHotPostsResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Discover.GetHotPosts(ctx, connect.NewRequest(&discoverv1.GetHotPostsRequest{Cursor: req.GetCursor()}))
	if err != nil {
		u.log.Error("discover.GetHotPosts failed", "error", err)
		return &bffv1.GetHotPostsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetHotPostsResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	cards, errCode := u.postCardsByIDs(ctx, resp.Msg.GetPostIds())
	if errCode != nil {
		return &bffv1.GetHotPostsResponse{Fail: true, Err: errCode}, nil
	}
	return &bffv1.GetHotPostsResponse{Posts: cards, Cursor: resp.Msg.GetCursor()}, nil
}

// GetRecommendFeed 推荐流：discover 取推荐 post_ids（seen 去重 + 打散 + 探索位）→ 组装卡片。
// uid 由会话注入（seen 账本归属）；游标透传 discover
func (u *BFFUsecaseImpl) GetRecommendFeed(ctx context.Context, req *bffv1.GetRecommendFeedRequest) (*bffv1.GetRecommendFeedResponse, error) {
	uid, errCode := u.sessionUID(ctx)
	if errCode != nil {
		return &bffv1.GetRecommendFeedResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Discover.GetRecommendFeed(ctx, connect.NewRequest(&discoverv1.GetRecommendFeedRequest{
		Uid: uid, Cursor: req.GetCursor(),
	}))
	if err != nil {
		u.log.Error("discover.GetRecommendFeed failed", "uid", uid, "error", err)
		return &bffv1.GetRecommendFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.GetRecommendFeedResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	cards, errCode := u.postCardsByIDs(ctx, resp.Msg.GetPostIds())
	if errCode != nil {
		return &bffv1.GetRecommendFeedResponse{Fail: true, Err: errCode}, nil
	}
	return &bffv1.GetRecommendFeedResponse{Posts: cards, Cursor: resp.Msg.GetCursor()}, nil
}
