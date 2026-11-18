package v1

import (
	"context"
	"fmt"
	"log/slog"
	"maps"

	"rednote/config"
	postv1 "rednote/gen/go/post/v1"
	media "rednote/internal/media/models"
	models "rednote/internal/post/models"
	"rednote/packages/cache"
	"rednote/packages/logger"

	"gorm.io/datatypes"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// CachedPostRepo PostRepository 的缓存装饰器（cache-aside）。key 构造/序列化/空哨兵/TTL 抖动
// 全部收口在 cache 层 typed API，本文件只管“读哪几层、写后失效谁”。
//
// 缓存范围：GetPosts 的非计数列（post:detail:{id}，单 post 粒度），含正文/状态/媒体关联
// （Medias 随实体整体往返，顺序不丢）。状态入缓存：BFF 按 PostCard 口径过滤，
// 缓存不得只存可见帖。游标分页与 GetPostMedias/GetPostCover 一期直通。
//
// 只读契约：返回实体是缓存/回源对象的浅拷贝，Medias（顺序即图片展示顺序）与 MetaData
// 与共享对象同底——调用方禁止重排序/原地修改（见 assemblePosts 与 models.PostDetail）。
//
// 统计列（like/favourite/comment/view）分块缓存（post:stat:{id}，短 TTL；stat_ttl < 0 禁用）：
// 计数读写频率与陈旧容忍度都和正文不同，且实体入库前计数已清零，故只有详情命中者需要统计列。
//   - 回源即权威：回源行是全列查询、自带计数 → 回源者直接采信，不读也不写 stat 缓存
//     （拿更旧的 stat 副本覆盖手上的精确值没有意义）；
//   - 详情命中者：计数为零值 → stat 短 TTL 读缓存（miss 合并回源 + 回填），写路径零失效，
//     陈旧 ≤ stat_ttl；空哨兵不用于 stat（stat 存在 ⟺ 行存在，“不存在”由 detail 层裁决）；
//   - 契约：PostRepoImpl.GetPosts 必须返回统计列——改成列裁剪会让回源者计数静默变零。
//
// 一致性（有界陈旧）：并发同 key 回源经 cache.BatchFlight 合并（detail/stat 各一条 flight）；
// 同一响应内回源者计数精确、命中者陈旧 ≤ stat_ttl；写后 DEL 对应 key（事务内为 commit 前）；
// 事务模式（WithTX/Transaction）读直穿不缓存；任何缓存错误只 warn 并回源 DB，不失败业务。
type CachedPostRepo struct {
	inner PostRepository
	c     cache.PostEntityCache
	log   *slog.Logger

	// txMode 事务绑定模式：读直穿不缓存
	txMode bool

	// bfDetail 合并并发相同详情回源（防击穿：per-key 合并 + 并集批查），fetch 构造时绑定
	bfDetail *cache.BatchFlight[int64, *models.PostDetail]

	// bfStat 合并并发相同统计列回源（同 bfDetail；statCache 禁用时为 nil，读取前须过开关）
	bfStat *cache.BatchFlight[int64, *models.PostStat]

	// statCache 统计列缓存开关（cache.stat_ttl < 0 禁用 → 命中者直通轻查）
	statCache bool
}

var _ PostRepository = (*CachedPostRepo)(nil)

// NewCachedPostRepo 创建帖子存储库缓存装饰器。
// TTL/序列化策略由 PostEntityCache 持有（config 其次 defaults），本装饰器不再关心。
// cfg 为 nil 时回源合并器用默认参数（config > defaults 分层在 cache.NewBatchFlight 内建）。
func NewCachedPostRepo(inner *PostRepoImpl, c cache.PostEntityCache, cfg *config.Config, lf logger.LoggerFactory) *CachedPostRepo {
	if inner == nil {
		panic(fmt.Errorf("cache: inner post repo is required"))
	}
	if c == nil {
		panic(fmt.Errorf("cache: cache client is required"))
	}
	r := &CachedPostRepo{
		inner: inner,
		c:     c,
		log:   lf.GetLoggerOf("post.repo.cache"),
		// 统计列短 TTL 读缓存默认启用；stat_ttl < 0 禁用（回退直通轻查）
		statCache: cfg == nil || cfg.Cache.StatTTL >= 0,
	}
	// 回源合并器：fetch 构造时绑定（方法值调用时读 r.inner，TX 克隆不触碰）
	var cc config.CacheConfig
	if cfg != nil {
		cc = cfg.Cache
	}
	r.bfDetail = cache.NewBatchFlight(cc.FlightWorkers, cc.FlightBatchSize, cc.FlightTimeout, r.fetchDetailAndFill)
	// 禁用 stat 缓存时不建 flight：否则常驻空转 worker 且无人收敛
	if r.statCache {
		r.bfStat = cache.NewBatchFlight(cc.FlightWorkers, cc.FlightBatchSize, cc.FlightTimeout, r.fetchStatAndFill)
	}
	return r
}

// bindInner 返回共享配置、替换 inner 并置 txMode 的副本（事务绑定用）
func (r *CachedPostRepo) bindInner(inner PostRepository) *CachedPostRepo {
	clone := *r // 结构体浅拷贝：flight 指针与 outer 共享，配置字段只读
	clone.inner = inner
	clone.txMode = true
	return &clone
}

// WithTX 返回绑定事务的装饰器（txMode）：
// 读直穿不缓存（事务内必须读到自身未提交的写入），写照常登记失效。
func (r *CachedPostRepo) WithTX(tx *gorm.DB) PostRepository {
	return r.bindInner(r.inner.WithTX(tx))
}

// Transaction 在事务内执行 fn：fn 拿到的是绑定事务的装饰器（txMode）而非裸 tx repo，
// 否则事务内写入会绕过失效登记（失效旁路）。失效时机同口径：commit 前 DEL。
func (r *CachedPostRepo) Transaction(fn func(repo PostRepository) error) error {
	return r.inner.Transaction(func(repo PostRepository) error {
		return fn(r.bindInner(repo))
	})
}

// DB 返回当前绑定的 *gorm.DB（供 MQ.SendTX 等事务性写入共享同一事务），直通
func (r *CachedPostRepo) DB() *gorm.DB {
	return r.inner.DB()
}

// ==================== 读路径 ====================

// GetPosts 批量获取帖子（cache-aside，按 post_id 粒度缓存非计数列）。
// 统计列分层：回源者采信回源行计数（精确），详情命中者走 stat 短 TTL 读缓存（陈旧 ≤ stat_ttl）。
// 返回顺序按 postIDs 首次出现顺序去重排列（支持部分命中：不存在/未命中的 id 不出现在结果里）。
func (r *CachedPostRepo) GetPosts(ctx context.Context, postIDs []int64) ([]*models.PostDetail, error) {
	// 空入参直通（空集不进缓存）；事务模式直通（须见事务内自身写入）
	if len(postIDs) == 0 || r.txMode {
		return r.inner.GetPosts(ctx, postIDs)
	}

	// 按首次出现顺序去重：MGET 不重复取同 key，返回集每 id 至多一条
	order := cache.DedupeIDs(postIDs)

	// 1) 读详情缓存（fail-open：缓存故障整批当未命中）
	byID, hitIDs, missIDs := r.readDetailCache(ctx, order)

	// 2) 未命中回源（BatchFlight 合并防击穿）+ 回填。回源行自带统计列 → 计数即权威，
	//    这批 id 不再进步骤 3（见文件头「回源即权威」）
	if len(missIDs) > 0 {
		fetched, err := r.bfDetail.Do(ctx, missIDs)
		if err != nil {
			return nil, err
		}
		maps.Copy(byID, fetched)
	}

	// 3) 统计列只服务详情命中者（其缓存实体计数为零值）；缓存错误 fail-open、DB 错误上抛
	statByID, err := r.loadStats(ctx, hitIDs)
	if err != nil {
		return nil, err
	}

	// 4) 保序组装（拷贝实体后覆盖统计列）
	return assemblePosts(order, byID, statByID), nil
}

// readDetailCache 批量读详情缓存（fail-open）：返回命中实体、命中 id（保持输入序，供统计列取样）
// 与未命中 id（miss → 回源）。空哨兵命中 = 确认不存在：不进任何集合，也不回填。
func (r *CachedPostRepo) readDetailCache(ctx context.Context, ids []int64) (byID map[int64]*models.PostDetail, hitIDs, missIDs []int64) {
	cached, err := r.c.LoadPostDetails(ctx, ids)
	if err != nil {
		// 降级全量回源：缓存不可用不得失败业务请求
		r.log.Warn("cache load post details failed, fallback to full fetch", "count", len(ids), "error", err)
		return cache.SplitHits[models.PostDetail](ids, nil)
	}
	return cache.SplitHits(ids, cached)
}

// fetchDetailAndFill 详情回源 + 回填（bfDetail fetch，构造时绑定）：fetchIDs 为并集，
// 命中写实体、缺失写空哨兵；只有批执行者回填（合并掉的等待者不重复写）。
func (r *CachedPostRepo) fetchDetailAndFill(ctx context.Context, fetchIDs []int64) (map[int64]*models.PostDetail, error) {
	posts, err := r.inner.GetPosts(ctx, fetchIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*models.PostDetail, len(posts))
	for _, p := range posts {
		byID[p.ID] = p
	}
	r.fillDetailCache(ctx, fetchIDs, byID)
	return byID, nil
}

// fillDetailCache 回填详情缓存（批量 MSet，一轮 RTT）：命中写实体（cache 层只存非计数列、
// TTL 自带抖动）、缺失写空哨兵（StorePostDetails 内部按 TTL 分两批）。
// 回填失败只记 warn：缓存缺失即下次回源，不影响正确性。
func (r *CachedPostRepo) fillDetailCache(ctx context.Context, ids []int64, byID map[int64]*models.PostDetail) {
	hits := make(map[int64]*models.PostDetail, len(byID))
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		p, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		hits[id] = p
	}
	if err := r.c.StorePostDetails(ctx, hits, missing); err != nil {
		r.log.Warn("cache store post details failed, fallback to next fetch",
			"hits", len(hits), "missing", len(missing), "error", err)
	}
}

// loadStats 统计列读取：只服务详情命中者（回源者计数由回源行提供，不走这里）。
//   - statCache 启用：短 TTL 读缓存 → miss 经 bfStat 合并回源 + 回填（写路径不失效，陈旧 ≤ stat_ttl）；
//   - 禁用（stat_ttl < 0）：DB 轻查直通（历史行为）。
//
// 缓存错误一律 fail-open 降级轻查（warn）；轻查错误是真错误，上抛。
func (r *CachedPostRepo) loadStats(ctx context.Context, ids []int64) (map[int64]*models.PostStat, error) {
	if len(ids) == 0 {
		// 整页回源：统计列已随回源行到位 → 零缓存访问、零轻查
		return nil, nil
	}
	if !r.statCache {
		return r.queryStats(ctx, ids)
	}
	byID, missIDs := r.readStatCache(ctx, ids)
	if len(missIDs) == 0 {
		return byID, nil
	}
	fetched, err := r.bfStat.Do(ctx, missIDs)
	if err != nil {
		return nil, err
	}
	maps.Copy(byID, fetched)
	return byID, nil
}

// readStatCache 批量读统计列缓存（fail-open）：返回命中值与未命中 id。
// 空哨兵（正常不出现）按无值处理，不回源。
func (r *CachedPostRepo) readStatCache(ctx context.Context, ids []int64) (byID map[int64]*models.PostStat, missIDs []int64) {
	cached, err := r.c.LoadPostStats(ctx, ids)
	if err != nil {
		r.log.Warn("cache load post stats failed, fallback to full query", "count", len(ids), "error", err)
		cached = nil // 降级全量轻查
	}
	byID, _, missIDs = cache.SplitHits(ids, cached)
	return byID, missIDs
}

// queryStats 统计列 DB 轻查（列裁剪：id + 4 个计数列）为 id→stat。
func (r *CachedPostRepo) queryStats(ctx context.Context, ids []int64) (map[int64]*models.PostStat, error) {
	stats, err := r.inner.GetPostStat(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*models.PostStat, len(stats))
	for _, s := range stats {
		byID[s.ID] = s
	}
	return byID, nil
}

// fetchStatAndFill 统计列回源 + 回填（bfStat fetch，构造时绑定）。
// 只写命中值、不写空哨兵（stat 存在 ⟺ 行存在，“不存在”由 detail 层裁决）；
// 回填走批量口（MSet 一轮 RTT），失败只记 warn（下次回源 refresh）。
func (r *CachedPostRepo) fetchStatAndFill(ctx context.Context, fetchIDs []int64) (map[int64]*models.PostStat, error) {
	byID, err := r.queryStats(ctx, fetchIDs)
	if err != nil {
		return nil, err
	}
	if err := r.c.StorePostStats(ctx, byID); err != nil {
		r.log.Warn("cache store post stats failed", "count", len(byID), "error", err)
	}
	return byID, nil
}

// assemblePosts 按 order 保序组装：跳过未命中/空哨兵；统计列以 stat 覆盖。
// 回源者本就带着权威计数（statByID 无此 id，保持原值）；命中者的实体计数是零值，
// 只有 stat 也缺失（极端降级）才会退化成 0，属可接受的降级而非错误。
//
// 只读契约：返回的实体是 byID 对象的浅拷贝（飞行合并窗口内该对象对并发调用方共享），
// 因此 Medias/MetaData 与共享对象同底。Medias 顺序即图片展示顺序（按 sort_order 加载
// 且缓存往返保序）——**禁止调用方重排序/原地修改元素**，否则会与其他并发请求互相污染。
func assemblePosts(order []int64, byID map[int64]*models.PostDetail, statByID map[int64]*models.PostStat) []*models.PostDetail {
	posts := make([]*models.PostDetail, 0, len(order))
	for _, id := range order {
		p, ok := byID[id]
		if !ok {
			continue
		}
		out := *p
		if s, ok := statByID[id]; ok {
			out.LikeCount = s.LikeCount
			out.FavouriteCount = s.FavouriteCount
			out.CommentCount = s.CommentCount
			out.ViewCount = s.ViewCount
		}
		posts = append(posts, &out)
	}
	return posts
}

// ==================== 写路径（失效登记） ====================

// invalidate 写后失效：DEL 帖子详情缓存 key（幂等）。
// 事务内写为 commit 前 DEL——cache-aside 固有竞态下可能残留旧值，陈旧 ≤ TTL（产品接受有界陈旧）；
// 失效失败同样靠 TTL 兜底，不阻断业务。
func (r *CachedPostRepo) invalidate(ctx context.Context, ids ...int64) {
	if err := r.c.DelPostDetails(ctx, ids); err != nil {
		r.log.Warn("cache invalidate failed, fallback to ttl", "post_ids", ids, "error", err)
	}
}

// CreatePost 创建帖子（写后失效：清除查询遗留的空哨兵/旧值）
func (r *CachedPostRepo) CreatePost(ctx context.Context, post *models.PostDetail) error {
	err := r.inner.CreatePost(ctx, post)
	if err == nil {
		r.invalidate(ctx, post.ID)
	}
	return err
}

// DeletePost 物理删除软删帖子行（写后失效）
func (r *CachedPostRepo) DeletePost(ctx context.Context, authorID, postID int64) (gen.ResultInfo, error) {
	info, err := r.inner.DeletePost(ctx, authorID, postID)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return info, err
}

// SetPostStatus 流转帖子状态（写后失效：发布/状态变更后不可读到旧状态）
func (r *CachedPostRepo) SetPostStatus(ctx context.Context, postID int64, from, to postv1.PostStatus) (gen.ResultInfo, error) {
	info, err := r.inner.SetPostStatus(ctx, postID, from, to)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return info, err
}

// UpdateDraftTitle 更新草稿标题（写后失效）
func (r *CachedPostRepo) UpdateDraftTitle(ctx context.Context, postID int64, title string) (gen.ResultInfo, error) {
	info, err := r.inner.UpdateDraftTitle(ctx, postID, title)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return info, err
}

// UpdateDraftText 更新草稿正文（写后失效）
func (r *CachedPostRepo) UpdateDraftText(ctx context.Context, postID int64, text string) (gen.ResultInfo, error) {
	info, err := r.inner.UpdateDraftText(ctx, postID, text)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return info, err
}

// UpdateDraftMetaData 更新草稿元数据（写后失效）
func (r *CachedPostRepo) UpdateDraftMetaData(ctx context.Context, postID int64, metaData datatypes.JSONMap) (gen.ResultInfo, error) {
	info, err := r.inner.UpdateDraftMetaData(ctx, postID, metaData)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return info, err
}

// CreatePostMedias 创建帖子与媒体的关联（写后失效：媒体关联随实体入缓存）
func (r *CachedPostRepo) CreatePostMedias(ctx context.Context, postID int64, medias []*media.MediaFile) error {
	err := r.inner.CreatePostMedias(ctx, postID, medias)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return err
}

// DeletePostMedias 删除帖子与媒体的关联（写后失效）
func (r *CachedPostRepo) DeletePostMedias(ctx context.Context, postID int64) ([]int64, error) {
	assetIDs, err := r.inner.DeletePostMedias(ctx, postID)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return assetIDs, err
}

// SetPostBanned 封禁帖子（写后失效）
func (r *CachedPostRepo) SetPostBanned(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	info, err := r.inner.SetPostBanned(ctx, postID)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return info, err
}

// SetPostDeleted 标记删除帖子（写后失效）
func (r *CachedPostRepo) SetPostDeleted(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	info, err := r.inner.SetPostDeleted(ctx, postID)
	if err == nil {
		r.invalidate(ctx, postID)
	}
	return info, err
}

// UpdatePostLikeCount 更新点赞数（不触碰缓存：统计列不入详情缓存块，写路径不失效，
// 读侧陈旧 ≤ cache.stat_ttl）
func (r *CachedPostRepo) UpdatePostLikeCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostLikeCount(ctx, postID, count)
}

// UpdatePostFavouriteCount 更新收藏数（不触碰缓存：同 UpdatePostLikeCount，陈旧 ≤ stat_ttl）
func (r *CachedPostRepo) UpdatePostFavouriteCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostFavouriteCount(ctx, postID, count)
}

// UpdatePostCommentCount 更新评论数（不触碰缓存：同 UpdatePostLikeCount，陈旧 ≤ stat_ttl）
func (r *CachedPostRepo) UpdatePostCommentCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostCommentCount(ctx, postID, count)
}

// UpdatePostViewCount 更新浏览数（不触碰缓存：同 UpdatePostLikeCount，陈旧 ≤ stat_ttl）
func (r *CachedPostRepo) UpdatePostViewCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostViewCount(ctx, postID, count)
}

// ==================== 直通路径（不缓存） ====================

// GetPostStat 直通（恒走 DB 轻查、绕过 stat 缓存）：需要精确计数或不适用 GetPosts
// 统计口径（回源者采信回源行 / 命中者读 stat 缓存）时用本方法
func (r *CachedPostRepo) GetPostStat(ctx context.Context, postIDs []int64) ([]*models.PostStat, error) {
	return r.inner.GetPostStat(ctx, postIDs)
}

// GetPostStatus 直通（一期不缓存；状态已在详情缓存内，独立轻查二期可从缓存派生）
func (r *CachedPostRepo) GetPostStatus(ctx context.Context, postID int64) (bool, postv1.PostStatus, error) {
	return r.inner.GetPostStatus(ctx, postID)
}

// GetAuthorID 直通（列裁剪轻查，一期不缓存）
func (r *CachedPostRepo) GetAuthorID(ctx context.Context, postID int64) (bool, int64, error) {
	return r.inner.GetAuthorID(ctx, postID)
}

// LockDraftForUpdate 直通（行锁语义，不可缓存）
func (r *CachedPostRepo) LockDraftForUpdate(ctx context.Context, postID int64) (bool, error) {
	return r.inner.LockDraftForUpdate(ctx, postID)
}

// LockDraftMediasForUpdate 直通（行锁语义，不可缓存）
func (r *CachedPostRepo) LockDraftMediasForUpdate(ctx context.Context, postID int64) error {
	return r.inner.LockDraftMediasForUpdate(ctx, postID)
}

// GetPostBriefsByAuthor 直通（游标分页，key 组合爆炸、命中率低，一期不缓存）
func (r *CachedPostRepo) GetPostBriefsByAuthor(ctx context.Context, authorID int64, before int64, limit int64) ([]*models.PostDetail, error) {
	return r.inner.GetPostBriefsByAuthor(ctx, authorID, before, limit)
}

// GetPublishedPostBriefsByAuthor 直通（游标分页，一期不缓存）
func (r *CachedPostRepo) GetPublishedPostBriefsByAuthor(ctx context.Context, authorID int64, before int64, limit int64) ([]*models.PostDetail, error) {
	return r.inner.GetPublishedPostBriefsByAuthor(ctx, authorID, before, limit)
}

// GetPostMedias 直通（一期不缓存；媒体关联已在详情缓存内，二期可从缓存派生）
func (r *CachedPostRepo) GetPostMedias(ctx context.Context, postIDs []int64) ([]*models.PostMedia, error) {
	return r.inner.GetPostMedias(ctx, postIDs)
}

// GetPostCover 直通（一期不缓存，同 GetPostMedias）
func (r *CachedPostRepo) GetPostCover(ctx context.Context, postIDs []int64) ([]*models.PostMedia, error) {
	return r.inner.GetPostCover(ctx, postIDs)
}

// ==================== 互动（点赞/收藏明细） ====================
// 全部直通转发：明细行不入缓存；计数列走 stat 短 TTL 读缓存、写路径不失效
// （同 UpdatePostLikeCount 口径），故无失效登记需求

// CreateLike 直通（幂等插入）
func (r *CachedPostRepo) CreateLike(ctx context.Context, postID int64, userID int64) (bool, error) {
	return r.inner.CreateLike(ctx, postID, userID)
}

// DeleteLike 直通
func (r *CachedPostRepo) DeleteLike(ctx context.Context, postID int64, userID int64) (bool, error) {
	return r.inner.DeleteLike(ctx, postID, userID)
}

// CreateFavourite 直通（幂等插入）
func (r *CachedPostRepo) CreateFavourite(ctx context.Context, postID int64, userID int64) (bool, error) {
	return r.inner.CreateFavourite(ctx, postID, userID)
}

// DeleteFavourite 直通
func (r *CachedPostRepo) DeleteFavourite(ctx context.Context, postID int64, userID int64) (bool, error) {
	return r.inner.DeleteFavourite(ctx, postID, userID)
}

// CountFavouritesByUser 直通
func (r *CachedPostRepo) CountFavouritesByUser(ctx context.Context, userID int64) (int64, error) {
	return r.inner.CountFavouritesByUser(ctx, userID)
}

// IncPostLikeCount 直通（同 UpdatePostLikeCount：计数不入详情缓存、写路径不失效）
func (r *CachedPostRepo) IncPostLikeCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.IncPostLikeCount(ctx, postID)
}

// DecPostLikeCount 直通
func (r *CachedPostRepo) DecPostLikeCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.DecPostLikeCount(ctx, postID)
}

// IncPostFavouriteCount 直通
func (r *CachedPostRepo) IncPostFavouriteCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.IncPostFavouriteCount(ctx, postID)
}

// DecPostFavouriteCount 直通
func (r *CachedPostRepo) DecPostFavouriteCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.DecPostFavouriteCount(ctx, postID)
}

// ListLikedPostIDs 直通（明细不在缓存范围）
func (r *CachedPostRepo) ListLikedPostIDs(ctx context.Context, userID int64, before int64, limit int64) ([]*models.PostLike, error) {
	return r.inner.ListLikedPostIDs(ctx, userID, before, limit)
}

// ListFavouritePostIDs 直通
func (r *CachedPostRepo) ListFavouritePostIDs(ctx context.Context, userID int64, before int64, limit int64) ([]*models.PostFavourite, error) {
	return r.inner.ListFavouritePostIDs(ctx, userID, before, limit)
}

// IncPostViewCount 直通
func (r *CachedPostRepo) IncPostViewCount(ctx context.Context, postID int64, delta int64) (gen.ResultInfo, error) {
	return r.inner.IncPostViewCount(ctx, postID, delta)
}

// ==================== 评论 ====================
// 全部直通转发：评论明细不入缓存，计数列不缓存（同 UpdatePostCommentCount 口径），无失效需求

// CreateComment 直通（追加插入）
func (r *CachedPostRepo) CreateComment(ctx context.Context, comment *models.PostComment) error {
	return r.inner.CreateComment(ctx, comment)
}

// GetComment 直通
func (r *CachedPostRepo) GetComment(ctx context.Context, commentID int64) (bool, *models.PostComment, error) {
	return r.inner.GetComment(ctx, commentID)
}

// LockCommentForUpdate 直通
func (r *CachedPostRepo) LockCommentForUpdate(ctx context.Context, commentID int64) (bool, *models.PostComment, error) {
	return r.inner.LockCommentForUpdate(ctx, commentID)
}

// SetCommentDeleted 直通
func (r *CachedPostRepo) SetCommentDeleted(ctx context.Context, commentID int64) (gen.ResultInfo, error) {
	return r.inner.SetCommentDeleted(ctx, commentID)
}

// SetCommentsDeletedByRoot 直通
func (r *CachedPostRepo) SetCommentsDeletedByRoot(ctx context.Context, rootID int64) (gen.ResultInfo, error) {
	return r.inner.SetCommentsDeletedByRoot(ctx, rootID)
}

// IncCommentReplyCount 直通
func (r *CachedPostRepo) IncCommentReplyCount(ctx context.Context, commentID int64) (gen.ResultInfo, error) {
	return r.inner.IncCommentReplyCount(ctx, commentID)
}

// DecCommentReplyCount 直通
func (r *CachedPostRepo) DecCommentReplyCount(ctx context.Context, commentID int64) (gen.ResultInfo, error) {
	return r.inner.DecCommentReplyCount(ctx, commentID)
}

// IncPostCommentCount 直通（计数不触碰缓存）
func (r *CachedPostRepo) IncPostCommentCount(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.IncPostCommentCount(ctx, postID)
}

// DecPostCommentCount 直通
func (r *CachedPostRepo) DecPostCommentCount(ctx context.Context, postID int64, n int64) (gen.ResultInfo, error) {
	return r.inner.DecPostCommentCount(ctx, postID, n)
}

// ListComments 直通（评论不在缓存范围）
func (r *CachedPostRepo) ListComments(ctx context.Context, postID int64, before int64, limit int64) ([]*models.PostComment, error) {
	return r.inner.ListComments(ctx, postID, before, limit)
}

// ListReplies 直通
func (r *CachedPostRepo) ListReplies(ctx context.Context, rootID int64, before int64, limit int64) ([]*models.PostComment, error) {
	return r.inner.ListReplies(ctx, rootID, before, limit)
}

// DeletePostComments 直通（物理回收不触碰缓存）
func (r *CachedPostRepo) DeletePostComments(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.DeletePostComments(ctx, postID)
}

// DeletePostLikes 直通（点赞明细不入缓存，回收无需登记失效）
func (r *CachedPostRepo) DeletePostLikes(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.DeletePostLikes(ctx, postID)
}

// DeletePostFavourites 直通（收藏明细不入缓存，同 DeletePostLikes）
func (r *CachedPostRepo) DeletePostFavourites(ctx context.Context, postID int64) (gen.ResultInfo, error) {
	return r.inner.DeletePostFavourites(ctx, postID)
}
