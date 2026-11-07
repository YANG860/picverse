package v1

import (
	"context"

	bffv1 "rednote/gen/go/bff/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	postv1 "rednote/gen/go/post/v1"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
)

// ==================== 搜索 ====================

// postBriefFrom Post 全量实体裁剪为 PostBrief（封面 = 首张媒体，对齐 post 服务 PostBrief 语义），
// 供 postCards 复用组装卡片
func postBriefFrom(p *postv1.Post) *postv1.PostBrief {
	brief := &postv1.PostBrief{
		PostId:    p.GetPostId(),
		Title:     p.GetTitle(),
		AuthorId:  p.GetAuthorId(),
		Stat:      p.GetPostStat(),
		CreatedAt: p.GetCreatedAt(),
		Status:    p.GetStatus(),
	}
	if len(p.GetMedias()) > 0 {
		brief.Cover = p.GetMedias()[0]
	}
	return brief
}

// SearchPosts 帖子搜索：discover 检索（关键词 + 双排序 + offset 游标）→ 按命中顺序回查
// GetPostsByIDs 组装卡片。可见性兜底：同步窗口内已软删/封禁的命中在回查后过滤
// （仅 PUBLISHED 面客），页内条数可能少于 limit（近似口径）。
func (u *BFFUsecaseImpl) SearchPosts(ctx context.Context, req *bffv1.SearchPostsRequest) (*bffv1.SearchPostsResponse, error) {
	if _, errCode := u.sessionUID(ctx); errCode != nil {
		return &bffv1.SearchPostsResponse{Fail: true, Err: errCode}, nil
	}

	resp, err := u.Repo.Discover.SearchPosts(ctx, connect.NewRequest(&discoverv1.SearchPostsRequest{
		Keyword: req.GetKeyword(),
		Sort:    req.GetSort(),
		Cursor:  req.GetCursor(),
	}))
	if err != nil {
		u.log.Error("discover.SearchPosts failed", "keyword", req.GetKeyword(), "error", err)
		return &bffv1.SearchPostsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if resp.Msg.GetFail() {
		return &bffv1.SearchPostsResponse{Fail: true, Err: resp.Msg.GetErr()}, nil
	}

	hits := resp.Msg.GetHits()
	if len(hits) == 0 {
		return &bffv1.SearchPostsResponse{}, nil
	}

	// 按命中顺序批量回查（GetPostsByIDs 不保证顺序，按 post_id 对齐），过滤非 PUBLISHED
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.GetPostId()
	}
	postsResp, err := u.Repo.Post.GetPostsByIDs(ctx, connect.NewRequest(&postv1.GetPostsByIDsRequest{PostIds: ids}))
	if err != nil {
		u.log.Error("post.GetPostsByIDs failed", "post_ids", ids, "error", err)
		return &bffv1.SearchPostsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	published := make(map[string]*postv1.Post, len(ids))
	for _, r := range postsResp.Msg.GetResults() {
		if r.GetFail() || r.GetPost() == nil {
			u.log.Warn("search hit missing，降级跳过", "post_id", r.GetPostId(), "err_code", r.GetErr().GetCode())
			continue
		}
		if r.GetPost().GetStatus() != postv1.PostStatus_POST_STATUS_PUBLISHED {
			u.log.Warn("search hit not published，降级过滤", "post_id", r.GetPostId(), "status", r.GetPost().GetStatus())
			continue
		}
		published[r.GetPostId()] = r.GetPost()
	}

	// 保持 discover 命中顺序组装 briefs（搜索排序序即面客序）
	briefs := make([]*postv1.PostBrief, 0, len(hits))
	uids := make([]string, 0, len(hits))
	for _, h := range hits {
		p := published[h.GetPostId()]
		if p == nil {
			continue
		}
		briefs = append(briefs, postBriefFrom(p))
		uids = append(uids, p.GetAuthorId())
	}
	if len(briefs) == 0 {
		// 整页命中均被过滤（同步窗口内的删/封禁洪峰）：按翻页到底处理，调用方以空页终止
		u.log.Warn("search page fully filtered，按翻页到底处理", "keyword", req.GetKeyword(), "count", len(hits))
		return &bffv1.SearchPostsResponse{}, nil
	}

	authors, errCode := u.authorViews(ctx, uids)
	if errCode != nil {
		return &bffv1.SearchPostsResponse{Fail: true, Err: errCode}, nil
	}
	cards, errCode := u.postCards(ctx, briefs, authors)
	if errCode != nil {
		return &bffv1.SearchPostsResponse{Fail: true, Err: errCode}, nil
	}
	return &bffv1.SearchPostsResponse{Posts: cards, Cursor: resp.Msg.GetCursor()}, nil
}
