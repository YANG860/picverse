package v1

import (
	"context"

	bffv1 "rednote/gen/go/bff/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	"rednote/packages/errmsg"

	"connectrpc.com/connect"
)

// ==================== 搜索 ====================

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

	// 按命中顺序回查组装卡片（可见性过滤兑底，共用 postCardsByIDs）
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.GetPostId()
	}
	cards, errCode := u.postCardsByIDs(ctx, ids)
	if errCode != nil {
		return &bffv1.SearchPostsResponse{Fail: true, Err: errCode}, nil
	}
	if len(cards) == 0 {
		// 整页命中均被过滤（同步窗口内的删/封禁洪峰）：按翻页到底处理，调用方以空页终止
		u.log.Warn("search page fully filtered，按翻页到底处理", "keyword", req.GetKeyword(), "count", len(hits))
		return &bffv1.SearchPostsResponse{}, nil
	}
	return &bffv1.SearchPostsResponse{Posts: cards, Cursor: resp.Msg.GetCursor()}, nil
}
