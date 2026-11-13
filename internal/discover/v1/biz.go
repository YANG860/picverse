package v1

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"rednote/config"
	commonv1 "rednote/gen/go/common/v1"
	discoverv1 "rednote/gen/go/discover/v1"
	models "rednote/internal/discover/models"
	"rednote/packages/cache"
	"rednote/packages/defaults"
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
	// GetHotPosts 热榜取数：热池全序翻页（热度降序，offset 游标；榜单语义，不做 seen/个性化）
	GetHotPosts(ctx context.Context, req *discoverv1.GetHotPostsRequest) (*discoverv1.GetHotPostsResponse, error)
	// GetRecommendFeed 推荐流取数：热池候选 + seen 去重 + 同作者打散 + 探索位
	GetRecommendFeed(ctx context.Context, req *discoverv1.GetRecommendFeedRequest) (*discoverv1.GetRecommendFeedResponse, error)
	// SearchPosts 帖子搜索：检索 Meili posts 索引，综合/最新双排序，offset 游标
	SearchPosts(ctx context.Context, req *discoverv1.SearchPostsRequest) (*discoverv1.SearchPostsResponse, error)
}

// DiscoverUsecaseImpl 搜推业务逻辑实现
// Fanout 为关注流收件箱（push 供给线）；Timeline 为大V发件箱缓存（pull 供给线）；
// HotPool 为热榜池（热榜/推荐流共享候选池）；Seen 为推荐流已曝光账本
type DiscoverUsecaseImpl struct {
	Meili    meili.MeiliRepo
	Fanout   FanoutRepo
	Timeline cache.AuthorTimelineCache
	HotPool  *cache.HotPoolCache
	Seen     *cache.SeenCache
	log      *slog.Logger

	recommendScanFactor    int     // 候选扫描批次 = limit × factor
	recommendScatterWindow int     // 同作者打散滑窗
	recommendExploreRatio  float64 // 探索位比例
}

// NewDiscoverUsecase 创建搜推业务逻辑实例
// lf 为日志工厂；m 为 Meilisearch 工具仓库（索引查询/同步统一出入口）；
// fo/tl 为关注流两路供给（收件箱 DB + 发件箱缓存）；hp/seen 为推荐流候选池与已曝光账本；
// cfg 的 Recommend 节为 0 项回退 defaults/recommend.go 默认值
func NewDiscoverUsecase(lf logger.LoggerFactory, m meili.MeiliRepo,
	fo FanoutRepo, tl cache.AuthorTimelineCache, hp *cache.HotPoolCache, seen *cache.SeenCache,
	cfg *config.Config) *DiscoverUsecaseImpl {
	factor := cfg.Recommend.ScanBatchFactor
	if factor <= 0 {
		factor = defaults.RecommendScanBatchFactor
	}
	window := cfg.Recommend.ScatterWindow
	if window <= 0 {
		window = defaults.RecommendScatterWindow
	}
	ratio := cfg.Recommend.ExploreRatio
	if ratio <= 0 {
		ratio = defaults.RecommendExploreRatio
	}
	return &DiscoverUsecaseImpl{
		Meili:    m,
		Fanout:   fo,
		Timeline: tl,
		HotPool:  hp,
		Seen:     seen,
		log:      lf.GetLoggerOf("discover"),

		recommendScanFactor:    factor,
		recommendScatterWindow: window,
		recommendExploreRatio:  ratio,
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

// GetHotPosts 热榜取数：热池全序翻页（热度降序，offset 游标）。
// 榜单语义：不做 seen 去重/个性化（可重复看的排行）；短取即到底（池内全序、
// 服务端不剔除已删——BFF 回查过滤兜底）；池缺失降级空页（fail-soft）
func (u *DiscoverUsecaseImpl) GetHotPosts(ctx context.Context, req *discoverv1.GetHotPostsRequest) (*discoverv1.GetHotPostsResponse, error) {
	offset64, limit64 := utils.ParseOffsetCursor(req.GetCursor())
	offset, limit := int(offset64), int(limit64)
	ms, err := u.HotPool.TopN(ctx, offset, limit)
	if err != nil {
		u.log.Error("hot posts failed", "error", err)
		return &discoverv1.GetHotPostsResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	ids := make([]string, 0, len(ms))
	for _, e := range ms {
		ids = append(ids, strconv.FormatInt(e.PostID, 10))
	}
	resp := &discoverv1.GetHotPostsResponse{PostIds: ids}
	if len(ms) == limit { // 短取 = 到底（游标置空）
		resp.Cursor = utils.NewOffsetCursor(int64(offset+len(ms)), limit64)
	}
	return resp, nil
}

// GetRecommendFeed 推荐流取数：热池候选 + seen 去重 + 同作者打散 + 探索位。
// 游标 = offset（扫描位置）。**短取 ≠ 到底**（seen/打散跳候选，产出 < limit 只是本页不满）：
// 批内消费位置决定推进——批内未用尽/批满续游标；批用尽且批短（扫到池尾）才是到底（游标置空）。
// 容错：候选/seen 读写失败整体失败重试（seen 失败不当"未曝光"防重复、不丢记账防再推荐）；
// 探索位采样失败降级跳过（非红线）；产出顺序 = 打散后正文 + 探索位页尾
func (u *DiscoverUsecaseImpl) GetRecommendFeed(ctx context.Context, req *discoverv1.GetRecommendFeedRequest) (*discoverv1.GetRecommendFeedResponse, error) {
	uid, err := strconv.ParseInt(req.GetUid(), 10, 64)
	if err != nil || uid <= 0 {
		return &discoverv1.GetRecommendFeedResponse{Fail: true, Err: errmsg.InvalidArgument}, nil
	}
	offset64, limit64 := utils.ParseOffsetCursor(req.GetCursor())
	offset, limit := int(offset64), int(limit64)
	if limit <= 0 {
		limit = defaults.CursorLimit
	}

	// 1. 候选扫描（批次 = limit × factor，预留 seen/打散过滤跳过）
	scanBatch := limit * u.recommendScanFactor
	cands, err := u.HotPool.TopN(ctx, offset, scanBatch)
	if err != nil {
		u.log.Error("recommend candidates failed", "uid", uid, "error", err)
		return &discoverv1.GetRecommendFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
	}
	if len(cands) == 0 {
		return &discoverv1.GetRecommendFeedResponse{}, nil // 到底（空页空游标）
	}

	// 2. seen 过滤（失败整体重试，不当"未曝光"）
	ids := make([]int64, len(cands))
	for i, c := range cands {
		ids[i] = c.PostID
	}
	seen, err := u.Seen.Filter(ctx, uid, ids)
	if err != nil {
		u.log.Error("recommend seen filter failed", "uid", uid, "error", err)
		return &discoverv1.GetRecommendFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	// 3. 探索位配额（页内混入，页大小稳定 = limit）；正文配额 = limit - exploreN
	exploreN := max(int(float64(limit)*u.recommendExploreRatio), 1)
	if exploreN >= limit {
		exploreN = limit - 1 // limit=1 无探索位
	}
	quota := limit - exploreN

	// 4. 顺序收集新鲜候选至正文配额，记录批内消费位置（已曝光跳过也算消费——游标精确推进不漏）
	fresh := make([]cache.HotEntry, 0, quota)
	consumed := 0
	for i, c := range cands {
		consumed = i + 1
		if seen[i] {
			continue
		}
		fresh = append(fresh, c)
		if len(fresh) == quota {
			break
		}
	}

	// 5. 同作者打散（滑窗内同作者 ≤ 1）+ 探索位（随机采样过 seen、去本页与互相重复）
	scatterEntries(fresh, u.recommendScatterWindow)
	explore, err := u.exploreEntries(ctx, uid, fresh, exploreN)
	if err != nil {
		u.log.Error("recommend explore failed", "uid", uid, "error", err)
		return &discoverv1.GetRecommendFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	// 6. 产出 + 曝光记账（失败整体重试，不丢记账）
	out := append(fresh, explore...)
	outIDs := make([]int64, len(out))
	for i, e := range out {
		outIDs[i] = e.PostID
	}
	if err := u.Seen.Mark(ctx, uid, outIDs); err != nil {
		u.log.Error("recommend seen mark failed", "uid", uid, "error", err)
		return &discoverv1.GetRecommendFeedResponse{Fail: true, Err: errmsg.InternalError}, nil
	}

	resp := &discoverv1.GetRecommendFeedResponse{PostIds: make([]string, len(out))}
	for i, e := range out {
		resp.PostIds[i] = strconv.FormatInt(e.PostID, 10)
	}
	// 7. 游标推进：批内未用尽或批满 → 续；批用尽且批短（池尾扫完）→ 到底（游标置空）
	if consumed < len(cands) || len(cands) == scanBatch {
		resp.Cursor = utils.NewOffsetCursor(int64(offset+consumed), limit64)
	}
	return resp, nil
}

// exploreEntries 探索位：随机采样 → 去本页/互相重复 → 过 seen → 至多 n 条。
// 采样失败降级空（非红线）；seen 失败向上抛（与主路径同口径：不当"未曝光"）
func (u *DiscoverUsecaseImpl) exploreEntries(ctx context.Context, uid int64, page []cache.HotEntry, n int) ([]cache.HotEntry, error) {
	if n <= 0 {
		return nil, nil
	}
	samples, err := u.HotPool.Sample(ctx, n*2) // 采多余留过滤余量
	if err != nil {
		u.log.Warn("explore sample failed，降级跳过探索位", "uid", uid, "error", err)
		return nil, nil
	}
	inPage := make(map[int64]bool, len(page)+len(samples))
	for _, e := range page {
		inPage[e.PostID] = true
	}
	picked := make([]cache.HotEntry, 0, n)
	pickedIDs := make([]int64, 0, n)
	for _, s := range samples {
		if inPage[s.PostID] {
			continue
		}
		inPage[s.PostID] = true
		picked = append(picked, s)
		pickedIDs = append(pickedIDs, s.PostID)
		if len(picked) == n {
			break
		}
	}
	if len(picked) == 0 {
		return nil, nil
	}
	seen, err := u.Seen.Filter(ctx, uid, pickedIDs)
	if err != nil {
		return nil, fmt.Errorf("explore seen filter uid %d: %w", uid, err)
	}
	out := picked[:0]
	for i, s := range picked {
		if !seen[i] {
			out = append(out, s)
		}
	}
	return out, nil
}

// scatterEntries 同作者打散：滑窗 window 内同作者 ≤ 1（贪心——当前与窗口冲突则与
// 批内第一个不冲突者交换；保持集合不变，只动顺序）
func scatterEntries(entries []cache.HotEntry, window int) {
	if window <= 1 || len(entries) <= 1 {
		return
	}
	conflict := func(pos, cand int) bool {
		for j := pos - window + 1; j < pos; j++ {
			if j >= 0 && entries[j].AuthorID == entries[cand].AuthorID {
				return true
			}
		}
		return false
	}
	for i := range entries {
		if !conflict(i, i) {
			continue
		}
		for k := i + 1; k < len(entries); k++ {
			if conflict(i, k) {
				continue
			}
			entries[i], entries[k] = entries[k], entries[i]
			break
		}
	}
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
