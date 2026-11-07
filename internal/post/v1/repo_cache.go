package v1

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"

	postv1 "rednote/gen/go/post/v1"
	media "rednote/internal/media/models"
	models "rednote/internal/post/models"
	"rednote/packages/cache"
	"rednote/packages/logger"

	"golang.org/x/sync/singleflight"
	"gorm.io/datatypes"
	"gorm.io/gen"
	"gorm.io/gorm"
)

// dedupeIDs 按首次出现顺序去重
func dedupeIDs(ids []int64) []int64 {
	order := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		order = append(order, id)
	}
	return order
}

// CachedPostRepo PostRepository 的缓存装饰器（cache-aside），基于 cache.PostEntityCache
// typed API 存取（key 构造/序列化/空哨兵/TTL 抖动均收口在 cache 层），与 CachedUserRepo 同一定式。
//
// 缓存范围：GetPosts 的非计数列（post:detail:{id}，单 key 粒度）——含正文、状态、
// 媒体关联（Medias 随实体整体序列化，顺序按 sort_order 往返不丢）。
//   - 统计列（like/favourite/comment/view）暂不缓存：GetPostStat 直通轻查，GetPosts
//     组装时每回轻查覆盖计数——保证精确，互动事件（SyncPostStat/IncrPostViewCount）
//     因此完全不触碰缓存。若后续统计轻查成为热点，再启用 PostEntityCache.Load/StorePostStat。
//   - 状态（Status）入缓存：GetPosts 返回任意状态、BFF 按 PostCard 口径过滤，
//     缓存不得只存可见帖。
//   - 游标分页（GetPostBriefsByAuthor 等）与 GetPostMedias/GetPostCover 一期直通不缓存
//     （key 组合爆炸/可从详情缓存派生）。
//
// 一致性模型（与 CachedUserRepo 同口径）——接受有界陈旧：
//   - 读：批量 cache-aside，部分命中只回源缺失；miss 回填空哨兵防穿透；
//     并发相同回源经 singleflight 合并防击穿；TTL ±10% 抖动防雪崩（cache 层内建）
//   - 写：方法返回后 DEL 对应 key（事务内写为 commit 前 DEL）；写后读通常即刻可见
//   - 事务模式（WithTX/Transaction）：读直穿不缓存——事务内必须读到自身未提交的写入；
//     写照常登记失效（Transaction 包装 fn 注入事务绑定装饰器，防失效旁路）
//   - 降级：任何缓存错误只记 warn 并回源 DB，绝不因缓存故障失败业务请求。
type CachedPostRepo struct {
	inner PostRepository
	c     cache.PostEntityCache
	log   *slog.Logger

	// txMode 事务绑定模式：读直穿不缓存
	txMode bool

	// sf 合并并发相同回源（防击穿）
	sf *singleflight.Group
}

var _ PostRepository = (*CachedPostRepo)(nil)

// NewCachedPostRepo 创建帖子存储库缓存装饰器。
// TTL/序列化策略由 PostEntityCache 持有（config 其次 defaults），本装饰器不再关心。
func NewCachedPostRepo(inner *PostRepoImpl, c cache.PostEntityCache, lf logger.LoggerFactory) *CachedPostRepo {
	if inner == nil {
		panic(fmt.Errorf("cache: inner post repo is required"))
	}
	if c == nil {
		panic(fmt.Errorf("cache: cache client is required"))
	}
	return &CachedPostRepo{
		inner: inner,
		c:     c,
		log:   lf.GetLoggerOf("post.repo.cache"),
		sf:    &singleflight.Group{},
	}
}

// bindInner 返回共享配置、替换 inner 并置 txMode 的副本（事务绑定用）
func (r *CachedPostRepo) bindInner(inner PostRepository) *CachedPostRepo {
	clone := *r // 结构体浅拷贝：sf 指针与 outer 共享，配置字段只读
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
// 统计列每次经 GetPostStat 轻查覆盖（暂不缓存，精确）。
// 返回顺序按 postIDs 首次出现顺序去重排列（与 GetPosts 的部分命中语义一致）。
func (r *CachedPostRepo) GetPosts(ctx context.Context, postIDs []int64) ([]*models.PostDetail, error) {
	// 空入参直通（空集不进缓存）；事务模式直通（须见事务内自身写入）
	if len(postIDs) == 0 || r.txMode {
		return r.inner.GetPosts(ctx, postIDs)
	}

	// 按首次出现顺序去重：MGET 不重复取同 key，返回集每 id 至多一条
	order := dedupeIDs(postIDs)

	// 1) 批量读缓存；缓存错误降级为全量回源（fail-open）
	byID := make(map[int64]*models.PostDetail, len(order))
	missing := make([]int64, 0, len(order))
	if cached, err := r.c.LoadPostDetails(ctx, order); err != nil {
		r.log.Warn("cache load post details failed，降级全量回源", "error", err)
		missing = append(missing, order...)
	} else {
		for _, id := range order {
			p, ok := cached[id]
			if !ok {
				missing = append(missing, id) // miss 回源
				continue
			}
			if p == nil {
				continue // 空哨兵：确认不存在，不回源不回填
			}
			byID[id] = p
		}
	}

	// 2) 缺失项回源并回填
	if len(missing) > 0 {
		fetched, err := r.fetchMissing(ctx, missing)
		if err != nil {
			return nil, err
		}
		maps.Copy(byID, fetched)
	}

	// 3) 统计列暂不缓存：每次轻查 GetPostStat（精确，互动事件无需失效缓存）
	statByID := make(map[int64]*models.PostStat, len(byID))
	if len(byID) > 0 {
		foundIDs := make([]int64, 0, len(order))
		for _, id := range order {
			if _, ok := byID[id]; ok {
				foundIDs = append(foundIDs, id)
			}
		}
		stats, err := r.inner.GetPostStat(ctx, foundIDs)
		if err != nil {
			return nil, err
		}
		for _, s := range stats {
			statByID[s.ID] = s
		}
	}

	// 4) 按入参首次出现顺序组装。
	// 拷贝后覆盖统计列：singleflight 合并窗口内共享的回源结果对象只读，
	// 直接改写会与其它并发调用方构成数据竞态。
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
	return posts, nil
}

// fetchMissing 回源查询缺失 id 并回填缓存。
// singleflight 按有序 id 集合合并并发相同回源（同一热 key 过期后的击穿流量只落库一次）；
// 合并窗口内共享结果对象，调用方按只读使用（组装时拷贝，见 GetPosts 步骤 4）。
func (r *CachedPostRepo) fetchMissing(ctx context.Context, ids []int64) (map[int64]*models.PostDetail, error) {
	sorted := append([]int64(nil), ids...)
	slices.Sort(sorted)
	strs := make([]string, len(sorted))
	for i, id := range sorted {
		strs[i] = strconv.FormatInt(id, 10)
	}

	v, err, _ := r.sf.Do("d:"+strings.Join(strs, ","), func() (any, error) {
		posts, err := r.inner.GetPosts(ctx, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[int64]*models.PostDetail, len(posts))
		for _, p := range posts {
			byID[p.ID] = p
		}
		r.fillCache(ctx, ids, byID)
		return byID, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(map[int64]*models.PostDetail), nil
}

// fillCache 回填缓存：命中的写实体（cache 层只存非计数列、TTL 自带抖动），
// 缺失的写空哨兵（StorePostDetail(ctx, id, nil)）。
// 回填失败只记 warn：缓存缺失即下次回源，不影响正确性。
func (r *CachedPostRepo) fillCache(ctx context.Context, ids []int64, byID map[int64]*models.PostDetail) {
	for _, id := range ids {
		p, ok := byID[id]
		if !ok {
			if err := r.c.StorePostDetail(ctx, id, nil); err != nil {
				r.log.Warn("cache store null sentinel failed", "post_id", id, "error", err)
			}
			continue
		}
		if err := r.c.StorePostDetail(ctx, id, p); err != nil {
			r.log.Warn("cache store post detail failed", "post_id", id, "error", err)
		}
	}
}

// ==================== 写路径（失效登记） ====================

// invalidate 写后失效：DEL 帖子详情缓存 key（幂等）。
// 事务内写为 commit 前 DEL——cache-aside 固有竞态下可能残留旧值，陈旧 ≤ TTL（产品接受有界陈旧）；
// 失效失败同样靠 TTL 兜底，不阻断业务。
func (r *CachedPostRepo) invalidate(ctx context.Context, ids ...int64) {
	if err := r.c.DelPostDetails(ctx, ids); err != nil {
		r.log.Warn("cache invalidate failed，降级靠 TTL 兜底", "post_ids", ids, "error", err)
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

// UpdatePostLikeCount 更新点赞数（不触碰缓存：统计列不在缓存实体内，计数经 GetPostStat 实时覆盖）
func (r *CachedPostRepo) UpdatePostLikeCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostLikeCount(ctx, postID, count)
}

// UpdatePostFavouriteCount 更新收藏数（不触碰缓存：同 UpdatePostLikeCount）
func (r *CachedPostRepo) UpdatePostFavouriteCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostFavouriteCount(ctx, postID, count)
}

// UpdatePostCommentCount 更新评论数（不触碰缓存：同 UpdatePostLikeCount）
func (r *CachedPostRepo) UpdatePostCommentCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostCommentCount(ctx, postID, count)
}

// UpdatePostViewCount 更新浏览数（不触碰缓存：同 UpdatePostLikeCount）
func (r *CachedPostRepo) UpdatePostViewCount(ctx context.Context, postID int64, count int64) (gen.ResultInfo, error) {
	return r.inner.UpdatePostViewCount(ctx, postID, count)
}

// ==================== 直通路径（不缓存） ====================

// GetPostStat 直通（统计列暂不缓存：每次 DB 轻查保证精确；
// 若轻查成为热点，再启用 PostEntityCache.Load/StorePostStat 升级写回式缓存）
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
// 全部直通转发：明细行不入缓存，计数列不缓存（同 UpdatePostLikeCount 口径），无失效需求

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

// IncPostLikeCount 直通（同 UpdatePostLikeCount：计数不触碰缓存）
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
