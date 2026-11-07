package v1

import (
	"context"
	"errors"
	"log/slog"

	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	"rednote/packages/errmsg"
	"rednote/packages/logger"
	"rednote/packages/meili"
	"rednote/packages/utils"
)

// ErrNotImplemented 脚手架占位错误，P1/P2 实现落地后移除
var ErrNotImplemented = errors.New("discover: not implemented")

// DiscoverUsecase 搜推业务逻辑接口
// 只输出 post_ids / 排序信息；可见性过滤与面客组装收敛在 BFF
type DiscoverUsecase interface {
	// GetFollowingFeed 关注流取数：push（fanout_timelines）+ pull（大V现拉）复合游标 2 路归并（P1 实现）
	GetFollowingFeed(ctx context.Context, req *discoverv1.GetFollowingFeedRequest) (*discoverv1.GetFollowingFeedResponse, error)
	// GetRecommendFeed 推荐流取数：热度快照 + seen 去重 + 同作者打散 + 探索位（P2 实现）
	GetRecommendFeed(ctx context.Context, req *discoverv1.GetRecommendFeedRequest) (*discoverv1.GetRecommendFeedResponse, error)
	// SearchPosts 帖子搜索：检索 Meili posts 索引，综合/最新双排序，offset 游标
	SearchPosts(ctx context.Context, req *discoverv1.SearchPostsRequest) (*discoverv1.SearchPostsResponse, error)
}

// DiscoverUsecaseImpl 搜推业务逻辑实现
type DiscoverUsecaseImpl struct {
	Meili meili.MeiliRepo
	log   *slog.Logger
}

// NewDiscoverUsecase 创建搜推业务逻辑实例
// lf 为日志工厂；m 为 Meilisearch 工具仓库（索引查询/同步统一出入口）
func NewDiscoverUsecase(lf logger.LoggerFactory, m meili.MeiliRepo) *DiscoverUsecaseImpl {
	return &DiscoverUsecaseImpl{
		Meili: m,
		log:   lf.GetLoggerOf("discover"),
	}
}

// GetFollowingFeed 关注流取数（P1 实现）：push（fanout_timelines）+ pull（大V现拉）复合游标 2 路归并
func (u *DiscoverUsecaseImpl) GetFollowingFeed(_ context.Context, _ *discoverv1.GetFollowingFeedRequest) (*discoverv1.GetFollowingFeedResponse, error) {
	// TODO(P1): fanout_timelines + discover_bigv_authors 2 路归并 + (created_at, id) 复合游标
	return nil, ErrNotImplemented
}

// GetRecommendFeed 推荐流取数（P2 实现）：热度快照 + seen 去重 + 同作者打散 + 探索位
func (u *DiscoverUsecaseImpl) GetRecommendFeed(_ context.Context, _ *discoverv1.GetRecommendFeedRequest) (*discoverv1.GetRecommendFeedResponse, error) {
	// TODO(P2): 热度快照 + seen 去重 + 同作者打散 + 探索位
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
