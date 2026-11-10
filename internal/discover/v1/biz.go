package v1

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"

	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	models "rednote/internal/discover/models"
	"rednote/packages/cache"
	"rednote/packages/errmsg"
	"rednote/packages/logger"
	"rednote/packages/meili"
	"rednote/packages/utils"
)

// ErrNotImplemented 尚未实现的 RPC 占位错误
var ErrNotImplemented = errors.New("discover: not implemented")

// DiscoverUsecase 搜推业务逻辑接口
// 只输出 post_ids / 排序信息；可见性过滤与面客组装收敛在 BFF
type DiscoverUsecase interface {
	// GetFollowingFeed 关注流取数：push（fanout_timelines）+ pull（大V现拉）复合游标 2 路归并
	GetFollowingFeed(ctx context.Context, req *discoverv1.GetFollowingFeedRequest) (*discoverv1.GetFollowingFeedResponse, error)
	// GetRecommendFeed 推荐流取数（尚未实现）
	GetRecommendFeed(ctx context.Context, req *discoverv1.GetRecommendFeedRequest) (*discoverv1.GetRecommendFeedResponse, error)
	// SearchPosts 帖子搜索：检索 Meili posts 索引，综合/最新双排序，offset 游标
	SearchPosts(ctx context.Context, req *discoverv1.SearchPostsRequest) (*discoverv1.SearchPostsResponse, error)
}

// DiscoverUsecaseImpl 搜推业务逻辑实现
// Fanout 为关注流收件箱（push 供给线）；Timeline 为大V发件箱缓存（pull 供给线）
type DiscoverUsecaseImpl struct {
	Meili    meili.MeiliRepo
	Fanout   FanoutRepo
	Timeline cache.AuthorTimelineCache
	log      *slog.Logger
}

// NewDiscoverUsecase 创建搜推业务逻辑实例
// lf 为日志工厂；m 为 Meilisearch 工具仓库（索引查询/同步统一出入口）；
// fo/tl 为关注流两路供给（收件箱 DB + 发件箱缓存）
func NewDiscoverUsecase(lf logger.LoggerFactory, m meili.MeiliRepo,
	fo FanoutRepo, tl cache.AuthorTimelineCache) *DiscoverUsecaseImpl {
	return &DiscoverUsecaseImpl{
		Meili:    m,
		Fanout:   fo,
		Timeline: tl,
		log:      lf.GetLoggerOf("discover"),
	}
}

// GetFollowingFeed 关注流取数：push（fanout_timelines）+ pull（大V发件箱缓存现拉）
// 复合游标 (created_at, post_id) 2 路归并；post_id 去重（边界双写兜底）。
// 零回源设计：缓存 = 承诺窗口（30d）内全量，短取即见底；删帖行不剔除（BFF 回查过滤兜底）。
// 到底判定：产出 < limit 即到底（游标置空，proto：post_ids 空且 cursor 空 = 到底）。
func (u *DiscoverUsecaseImpl) GetFollowingFeed(ctx context.Context, req *discoverv1.GetFollowingFeedRequest) (*discoverv1.GetFollowingFeedResponse, error) {
	uid, err := strconv.ParseInt(req.GetUid(), 10, 64)
	if err != nil {
		return &discoverv1.GetFollowingFeedResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}
	cursorTS, cursorID, limit := utils.ParseMergeCursor(req.GetCursor())

	// 路 A 收件箱（普通作者供给：写侧扇出的全量，DB 即源）
	inbox, err := u.Fanout.ReadInbox(ctx, uid, cursorTS, cursorID, int(limit))
	if err != nil {
		u.log.Error("following feed: read inbox failed", "uid", uid, "error", err)
		return &discoverv1.GetFollowingFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	// 路 B pull（大V供给：缓存存在性分流；短取 = 承诺窗口内见底，免回源）。
	// 缓存读失败不当作 miss（会丢大V供给线，宁重不漏）→ 整体失败重试
	pull := make([]cache.TimelineEntry, 0, len(req.GetFollowingIds())*2)
	for _, fid := range req.GetFollowingIds() {
		aid, err := strconv.ParseInt(fid, 10, 64)
		if err != nil {
			return &discoverv1.GetFollowingFeedResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
		}
		entries, miss, err := u.Timeline.Window(ctx, aid, cursorTS, cursorID, int(limit))
		if err != nil {
			u.log.Error("following feed: timeline window failed", "uid", uid, "author_id", aid, "error", err)
			return &discoverv1.GetFollowingFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
		}
		if miss {
			// 普通作者/超窗不活跃作者：帖子由收件箱路供给（或不在承诺窗口），非 pull 对象
			continue
		}
		pull = append(pull, entries...)
	}

	// 归并：(created_at, post_id) DESC + post_id 去重 + top-limit
	merged := mergeFeed(inbox, pull, limit)

	postIDs := make([]string, len(merged))
	for i, e := range merged {
		postIDs[i] = strconv.FormatInt(e.PostID, 10)
	}
	// 短页即到底（游标置空）；满页推进游标到末条
	var cursor *commonv1.Cursor
	if int64(len(merged)) == limit {
		last := merged[len(merged)-1]
		cursor = utils.NewMergeCursor(last.CreatedAt, last.PostID, limit)
	}
	return &discoverv1.GetFollowingFeedResponse{PostIds: postIDs, Cursor: cursor}, nil
}

// mergeFeed 两路归并：收件箱行 + 大V缓存窗按 (created_at, post_id) DESC 排序，
// post_id 去重（边界双写由归并去重兜底，宁重不漏），取前 limit 条
func mergeFeed(inbox []models.FanoutTimeline, pull []cache.TimelineEntry, limit int64) []cache.TimelineEntry {
	merged := make([]cache.TimelineEntry, 0, len(inbox)+len(pull))
	for _, row := range inbox {
		merged = append(merged, cache.TimelineEntry{PostID: row.PostID, CreatedAt: row.PostCreatedAt})
	}
	merged = append(merged, pull...)
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].CreatedAt != merged[j].CreatedAt {
			return merged[i].CreatedAt > merged[j].CreatedAt
		}
		return merged[i].PostID > merged[j].PostID
	})
	out := make([]cache.TimelineEntry, 0, limit)
	seen := make(map[int64]struct{}, limit)
	for _, e := range merged {
		if _, ok := seen[e.PostID]; ok {
			continue
		}
		seen[e.PostID] = struct{}{}
		out = append(out, e)
		if int64(len(out)) == limit {
			break
		}
	}
	return out
}

// GetRecommendFeed 推荐流取数（尚未实现）
func (u *DiscoverUsecaseImpl) GetRecommendFeed(_ context.Context, _ *discoverv1.GetRecommendFeedRequest) (*discoverv1.GetRecommendFeedResponse, error) {
	return nil, ErrNotImplemented
}

// SearchPosts 帖子搜索：检索 Meili posts 索引（仅 PUBLISHED 入列）。
// 双排序：综合（默认，相关度 × 时间衰减当页重排）/ 最新（时间倒序直出）；
// 配置组装/请求/解码/重排收敛在 packages/meili（SearchPosts / SearchLatestPosts），
// 本层只做 proto 转发与结果映射。游标编码 offset；空页（hits 为空）即翻页到底，不返回游标。
func (u *DiscoverUsecaseImpl) SearchPosts(ctx context.Context, req *discoverv1.SearchPostsRequest) (*discoverv1.SearchPostsResponse, error) {
	offset, limit := utils.ParseOffsetCursor(req.GetCursor())

	// 双排序收敛到 meili 领域接口，调用方只传 query 与游标（offset/limit）
	var (
		result *meili.PostSearchResult
		err    error
	)
	if req.GetSort() == discoverv1.SearchSort_SEARCH_SORT_LATEST {
		result, err = u.Meili.SearchLatestPosts(ctx, req.GetKeyword(), offset, limit)
	} else {
		result, err = u.Meili.SearchPosts(ctx, req.GetKeyword(), offset, limit)
	}
	if err != nil {
		u.log.Error("meili search failed", "keyword", req.GetKeyword(), "offset", offset, "error", err)
		return &discoverv1.SearchPostsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	hits := make([]*discoverv1.SearchHit, len(result.Hits))
	for i, h := range result.Hits {
		hits[i] = &discoverv1.SearchHit{PostId: h.PostID, Score: h.Score}
	}

	// 游标按 Meili 实际消费的偏移推进（NextOffset 含坏命中跳过口径）；空页即到底
	var cursor *commonv1.Cursor
	if result.NextOffset > 0 {
		cursor = utils.NewOffsetCursor(result.NextOffset, limit)
	}
	return &discoverv1.SearchPostsResponse{Hits: hits, Cursor: cursor}, nil
}
