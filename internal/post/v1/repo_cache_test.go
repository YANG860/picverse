package v1

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rednote/config"
	postv1 "rednote/gen/go/post/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	models "rednote/internal/post/models"
	"rednote/packages/cache"
	"rednote/packages/logger"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// cachedPostRepoEnv 缓存装饰器测试环境：真实 repo（testdb 克隆库）+ testredis 内存替身 +
// 真实 cache.PostEntityCache（序列化/空哨兵/TTL 真实链路）。
// 日志落临时文件（避免测试污染 stdout）。
type cachedPostRepoEnv struct {
	repo  *CachedPostRepo
	inner *PostRepoImpl
	db    *gorm.DB
	tc    *testredis.Client
	ec    cache.PostEntityCache
	lf    logger.LoggerFactory
}

func newCachedPostRepoEnv(t *testing.T) *cachedPostRepoEnv {
	t.Helper()
	inner, db := newRepo(t)
	tc := testredis.New()
	lf, cleanup := logger.NewLoggerFactory(&config.Config{
		Logger: config.LoggerConfig{File: true, Out: filepath.Join(t.TempDir(), "cache-test.log")},
	})
	t.Cleanup(cleanup)

	ec := cache.NewPostEntityCache(tc, &config.Config{
		Cache: config.CacheConfig{
			PostTTL: time.Hour,
			NullTTL: time.Minute,
			StatTTL: time.Hour, // 测试内不过期：陈旧语义用 DelPostStats 显式模拟过期
		},
	}, lf)
	return &cachedPostRepoEnv{
		repo:  NewCachedPostRepo(inner, ec, nil, lf),
		inner: inner,
		db:    db,
		tc:    tc,
		ec:    ec,
		lf:    lf,
	}
}

// postErrEntityCache 可注入错误的 cache.PostEntityCache 包装（降级路径用例的测试替身）：
// 各 err 非 nil 时对应操作返回该错误，否则委托真实实现。仅限单 goroutine 使用。
type postErrEntityCache struct {
	cache.PostEntityCache
	loadErr      error
	storeErr     error
	delErr       error
	statLoadErr  error
	statStoreErr error
}

func (e *postErrEntityCache) LoadPostStats(ctx context.Context, ids []int64) (map[int64]*models.PostStat, error) {
	if e.statLoadErr != nil {
		return nil, e.statLoadErr
	}
	return e.PostEntityCache.LoadPostStats(ctx, ids)
}

func (e *postErrEntityCache) StorePostStat(ctx context.Context, id int64, stat *models.PostStat) error {
	if e.statStoreErr != nil {
		return e.statStoreErr
	}
	return e.PostEntityCache.StorePostStat(ctx, id, stat)
}

// StorePostStats 统计列回填错误注入：装饰器走批量口，故须同时覆写批量口
func (e *postErrEntityCache) StorePostStats(ctx context.Context, stats map[int64]*models.PostStat) error {
	if e.statStoreErr != nil {
		return e.statStoreErr
	}
	return e.PostEntityCache.StorePostStats(ctx, stats)
}

func (e *postErrEntityCache) LoadPostDetails(ctx context.Context, ids []int64) (map[int64]*models.PostDetail, error) {
	if e.loadErr != nil {
		return nil, e.loadErr
	}
	return e.PostEntityCache.LoadPostDetails(ctx, ids)
}

func (e *postErrEntityCache) StorePostDetail(ctx context.Context, id int64, detail *models.PostDetail) error {
	if e.storeErr != nil {
		return e.storeErr
	}
	return e.PostEntityCache.StorePostDetail(ctx, id, detail)
}

// StorePostDetails 回填错误注入：装饰器走批量回填，故须同时覆写批量口
func (e *postErrEntityCache) StorePostDetails(ctx context.Context, hits map[int64]*models.PostDetail, missing []int64) error {
	if e.storeErr != nil {
		return e.storeErr
	}
	return e.PostEntityCache.StorePostDetails(ctx, hits, missing)
}

func (e *postErrEntityCache) DelPostDetails(ctx context.Context, ids []int64) error {
	if e.delErr != nil {
		return e.delErr
	}
	return e.PostEntityCache.DelPostDetails(ctx, ids)
}

// ---------- GetPosts 缓存读 ----------

func TestCachedGetPosts(t *testing.T) {
	env := newCachedPostRepoEnv(t)
	ctx := context.Background()

	t.Run("miss 回源并回填，二次读命中缓存", func(t *testing.T) {
		seedPost(t, env.db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)

		posts, err := env.repo.GetPosts(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "标题", posts[0].Title)

		// 已回填缓存（可经 typed API 读回）
		cached, err := env.ec.LoadPostDetails(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Contains(t, cached, postID1)
		require.NotNil(t, cached[postID1])
		assert.Equal(t, "标题", cached[postID1].Title)

		// 绕过装饰器改 DB：缓存命中应返回旧值，证明二次读未回源
		require.NoError(t, env.db.Model(&models.PostDetail{}).Where("id = ?", postID1).Update("title", "db-raw").Error)
		posts, err = env.repo.GetPosts(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "标题", posts[0].Title, "应命中缓存而非回源")
	})

	t.Run("全字段往返：缓存命中与回源结果一致（含媒体关联与顺序）", func(t *testing.T) {
		seedPost(t, env.db, postID2, authorID2, postv1.PostStatus_POST_STATUS_BANNED, 2000)
		// 乱序插入两条媒体关联，验证 sort_order 往返不丢序
		seedPostMedia(t, env.db, 8001, postID2, 7002, 7102, 2)
		seedPostMedia(t, env.db, 8002, postID2, 7001, 7101, 1)

		miss, err := env.repo.GetPosts(ctx, []int64{postID2})
		require.NoError(t, err)
		require.Len(t, miss, 1)
		hit, err := env.repo.GetPosts(ctx, []int64{postID2})
		require.NoError(t, err)
		require.Len(t, hit, 1)
		assert.Equal(t, miss[0], hit[0], "缓存往返不应丢失/篡改任何字段")

		// 全字段显式断言（对照 seedPost/seedPostMedia 的已知值）
		p := hit[0]
		assert.Equal(t, postID2, p.ID)
		assert.Equal(t, authorID2, p.AuthorID)
		assert.Equal(t, "标题", p.Title)
		assert.Equal(t, "正文", p.FullText)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_BANNED, p.Status)
		assert.Equal(t, datatypes.JSONMap{"k": "v"}, p.MetaData)
		assert.Equal(t, int64(1), p.LikeCount)
		assert.Equal(t, int64(2), p.FavouriteCount)
		assert.Equal(t, int64(3), p.CommentCount)
		assert.Equal(t, int64(4), p.ViewCount)
		require.Len(t, p.Medias, 2, "媒体关联应完整往返")
		assert.Equal(t, int32(1), p.Medias[0].SortOrder, "媒体应按 sort_order 排序往返")
		assert.Equal(t, int32(2), p.Medias[1].SortOrder)
		assert.Equal(t, int64(8002), p.Medias[0].ID)
		require.NotNil(t, p.Medias[0].Media, "内嵌媒体文件应完整往返")
		assert.Equal(t, int64(7001), p.Medias[0].Media.ID)
		assert.Equal(t, int64(7101), p.Medias[0].Media.AssetID)
		assert.Equal(t, "post/obj", p.Medias[0].Media.ObjKey)
		assert.Equal(t, "rednote", p.Medias[0].Media.Bucket)
		assert.Equal(t, int64(1024), p.Medias[0].Media.SizeBytes)
		assert.Equal(t, int64(100), p.Medias[0].Media.Height)
		assert.Equal(t, int64(200), p.Medias[0].Media.Width)
		assert.Equal(t, map[string]any{"k": "v"}, map[string]any(p.Medias[0].Media.MediaMetaData))
	})

	t.Run("部分命中只回填缺失项", func(t *testing.T) {
		seedPost(t, env.db, postID3, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 3000)
		// postID1 已缓存（detail + stat），postID3 未缓存
		sets := env.tc.Sets
		posts, err := env.repo.GetPosts(ctx, []int64{postID3, postID1})
		require.NoError(t, err)
		require.Len(t, posts, 2)
		assert.Equal(t, 1, env.tc.Sets-sets, "只应回填缺失项 postID3 的 detail（回源者不写 stat）")

		_, err = env.ec.LoadPostDetails(ctx, []int64{postID3})
		require.NoError(t, err)
		// 回源者不写 stat 缓存：其计数已由回源行提供
		sc, err := env.ec.LoadPostStats(ctx, []int64{postID3})
		require.NoError(t, err)
		assert.NotContains(t, sc, postID3, "回源者不回填 stat 缓存")
	})

	t.Run("回源即权威：计数采信回源行、不读也不写 stat 缓存", func(t *testing.T) {
		const id int64 = 2201
		seedPost(t, env.db, id, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 9000)

		gets, sets, msets := env.tc.Gets, env.tc.Sets, env.tc.MSets
		posts, err := env.repo.GetPosts(ctx, []int64{id})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		// 契约回归：回源行必须带统计列（若 GetPosts 改列裁剪，这里会静默变 0）
		assert.Equal(t, int64(1), posts[0].LikeCount)
		assert.Equal(t, int64(2), posts[0].FavouriteCount)
		assert.Equal(t, int64(3), posts[0].CommentCount)
		assert.Equal(t, int64(4), posts[0].ViewCount)
		assert.Equal(t, gets+1, env.tc.Gets, "只读 detail 一个 key（回源者不查 stat 缓存）")
		assert.Equal(t, sets+1, env.tc.Sets, "只写 detail 一次（回源者不回填 stat 缓存）")

		sc, err := env.ec.LoadPostStats(ctx, []int64{id})
		require.NoError(t, err)
		assert.NotContains(t, sc, id, "stat 缓存只由详情命中路径回填")

		// 二次读：详情命中 → 计数改走 stat 缓存（miss 轻查 + 回填）
		gets, sets, msets = env.tc.Gets, env.tc.Sets, env.tc.MSets
		posts, err = env.repo.GetPosts(ctx, []int64{id})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, int64(1), posts[0].LikeCount, "命中者经 stat 轻查得到同一计数")
		assert.Equal(t, int64(4), posts[0].ViewCount)
		assert.Equal(t, gets+2, env.tc.Gets, "detail + stat 各一次批量读")
		assert.Equal(t, sets+1, env.tc.Sets, "stat 缓存回填一次")
		assert.Equal(t, msets+1, env.tc.MSets, "stat 回填同样走批量 MSet（一轮 RTT）")
	})

	t.Run("重复 id 去重返回", func(t *testing.T) {
		posts, err := env.repo.GetPosts(ctx, []int64{postID1, postID1})
		require.NoError(t, err)
		assert.Len(t, posts, 1)
	})

	t.Run("结果按入参首次出现顺序组装", func(t *testing.T) {
		posts, err := env.repo.GetPosts(ctx, []int64{postID3, postID1, postID2, postID3})
		require.NoError(t, err)
		require.Len(t, posts, 3)
		assert.Equal(t, postID3, posts[0].ID, "结果应按入参首次出现顺序")
		assert.Equal(t, postID1, posts[1].ID)
		assert.Equal(t, postID2, posts[2].ID)
	})

	t.Run("空入参不访问缓存", func(t *testing.T) {
		gets, sets := env.tc.Gets, env.tc.Sets
		posts, err := env.repo.GetPosts(ctx, []int64{})
		require.NoError(t, err)
		assert.Empty(t, posts)
		assert.Equal(t, gets, env.tc.Gets, "空入参不应读缓存")
		assert.Equal(t, sets, env.tc.Sets, "空入参不应写缓存")
	})

	t.Run("批量回源回填合并为一次 MSet（一轮 RTT）", func(t *testing.T) {
		ids := []int64{2301, 2302, 2303}
		for _, id := range ids {
			seedPost(t, env.db, id, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 10000)
		}
		sets, msets := env.tc.Sets, env.tc.MSets
		posts, err := env.repo.GetPosts(ctx, ids)
		require.NoError(t, err)
		require.Len(t, posts, 3)
		assert.Equal(t, sets+3, env.tc.Sets, "每个 key 各一次写入")
		assert.Equal(t, msets+1, env.tc.MSets, "回填共一批：只占一轮 RTT")
	})

	t.Run("不存在 id 回填空哨兵，二次读不回源", func(t *testing.T) {
		msets := env.tc.MSets
		posts, err := env.repo.GetPosts(ctx, []int64{999999})
		require.NoError(t, err)
		assert.Empty(t, posts)
		assert.Equal(t, msets+1, env.tc.MSets, "空哨兵单独一批")

		// 空哨兵已立：map 中存在该 id 且值为 nil（确认不存在）
		cached, err := env.ec.LoadPostDetails(ctx, []int64{999999})
		require.NoError(t, err)
		require.Contains(t, cached, int64(999999))
		assert.Nil(t, cached[999999], "空哨兵应映射为 nil")

		// 二次读命中哨兵：不回源不回填（fillDetailCache 不再写）
		sets := env.tc.Sets
		posts, err = env.repo.GetPosts(ctx, []int64{999999})
		require.NoError(t, err)
		assert.Empty(t, posts)
		assert.Equal(t, sets, env.tc.Sets, "哨兵命中不应再次回填")
	})

	t.Run("统计列短 TTL 缓存：陈旧有界、过期即刷新", func(t *testing.T) {
		// postID2 已缓存：主体来自 detail 缓存，计数在上面的二次读（命中路径）回填了 stat
		cached, err := env.ec.LoadPostDetails(ctx, []int64{postID2})
		require.NoError(t, err)
		require.NotNil(t, cached[postID2])
		assert.Zero(t, cached[postID2].LikeCount, "统计列不入 detail 缓存")
		assert.Zero(t, cached[postID2].FavouriteCount, "统计列不入 detail 缓存")
		assert.Zero(t, cached[postID2].CommentCount, "统计列不入 detail 缓存")
		assert.Zero(t, cached[postID2].ViewCount, "统计列不入 detail 缓存")

		// 首读已回填 stat 缓存（typed API 可读回）
		sc, err := env.ec.LoadPostStats(ctx, []int64{postID2})
		require.NoError(t, err)
		require.NotNil(t, sc[postID2])
		assert.Equal(t, int64(1), sc[postID2].LikeCount)

		// 绕过装饰器改 DB：详情主体走缓存旧值，计数同样陈旧（写路径不失效，陈旧 ≤ stat_ttl）
		require.NoError(t, env.db.Model(&models.PostDetail{}).Where("id = ?", postID2).
			Update("title", "db-raw").
			Update("like_count", 100).
			Update("favourite_count", 200).
			Update("comment_count", 300).
			Update("view_count", 400).Error)
		posts, err := env.repo.GetPosts(ctx, []int64{postID2})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "标题", posts[0].Title, "详情主体命中缓存")
		assert.Equal(t, int64(1), posts[0].LikeCount, "TTL 内计数陈旧（stat 缓存命中，不打轻查）")
		assert.Equal(t, int64(2), posts[0].FavouriteCount)

		// 模拟 TTL 过期（DelPostStats）：下读 miss 轻查回填，新计数可见
		require.NoError(t, env.ec.DelPostStats(ctx, []int64{postID2}))
		posts, err = env.repo.GetPosts(ctx, []int64{postID2})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "标题", posts[0].Title, "详情主体仍命中缓存")
		assert.Equal(t, int64(100), posts[0].LikeCount, "过期后 miss 轻查刷新")
		assert.Equal(t, int64(200), posts[0].FavouriteCount)
		assert.Equal(t, int64(300), posts[0].CommentCount)
		assert.Equal(t, int64(400), posts[0].ViewCount)
	})

	t.Run("缓存读失败降级全量回源", func(t *testing.T) {
		seedPost(t, env.db, postID4, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 4000)
		cacheErr := errors.New("cache down")
		failRepo := NewCachedPostRepo(env.inner, &postErrEntityCache{PostEntityCache: env.ec, loadErr: cacheErr}, nil, env.lf)

		posts, err := failRepo.GetPosts(ctx, []int64{postID4})
		require.NoError(t, err, "缓存故障不应失败业务请求")
		require.Len(t, posts, 1)
		assert.Equal(t, "标题", posts[0].Title)
	})

	t.Run("回填失败不影响结果", func(t *testing.T) {
		seedPost(t, env.db, 2101, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 5000)
		cacheErr := errors.New("cache down")
		failRepo := NewCachedPostRepo(env.inner, &postErrEntityCache{PostEntityCache: env.ec, storeErr: cacheErr}, nil, env.lf)

		posts, err := failRepo.GetPosts(ctx, []int64{2101})
		require.NoError(t, err, "回填失败只应降级，不影响结果")
		require.Len(t, posts, 1)
		assert.Equal(t, "标题", posts[0].Title)
	})

	t.Run("统计列禁用（stat_ttl < 0）：命中者直通轻查", func(t *testing.T) {
		seedPost(t, env.db, 2102, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 6000)
		plain := NewCachedPostRepo(env.inner, env.ec, &config.Config{Cache: config.CacheConfig{StatTTL: -1}}, env.lf)
		assert.Nil(t, plain.bfStat, "stat 缓存禁用时不建 flight（避免常驻空转 worker）")

		posts, err := plain.GetPosts(ctx, []int64{2102}) // 首读回源：计数即回源行
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, int64(1), posts[0].LikeCount)

		require.NoError(t, env.db.Model(&models.PostDetail{}).Where("id = ?", 2102).Update("like_count", 77).Error)
		posts, err = plain.GetPosts(ctx, []int64{2102}) // 命中者：禁用后直通轻查
		require.NoError(t, err)
		assert.Equal(t, int64(77), posts[0].LikeCount, "禁用后计数实时（历史行为）")
	})

	t.Run("统计列缓存读失败降级轻查", func(t *testing.T) {
		seedPost(t, env.db, 2103, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 7000)
		// 首读回源（填 detail，不触 stat）；再构造 stat 读失败，验证命中路径 fail-open
		_, err := env.repo.GetPosts(ctx, []int64{2103})
		require.NoError(t, err)
		cacheErr := errors.New("cache down")
		failRepo := NewCachedPostRepo(env.inner, &postErrEntityCache{PostEntityCache: env.ec, statLoadErr: cacheErr}, nil, env.lf)

		posts, err := failRepo.GetPosts(ctx, []int64{2103})
		require.NoError(t, err, "统计缓存故障不应失败业务请求")
		require.Len(t, posts, 1)
		assert.Equal(t, int64(1), posts[0].LikeCount, "fail-open 轻查仍返回计数")
	})

	t.Run("统计列缓存回填失败不影响结果", func(t *testing.T) {
		seedPost(t, env.db, 2104, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 8000)
		_, err := env.repo.GetPosts(ctx, []int64{2104}) // 首读回源（填 detail，不触 stat）
		require.NoError(t, err)
		cacheErr := errors.New("cache down")
		failRepo := NewCachedPostRepo(env.inner, &postErrEntityCache{PostEntityCache: env.ec, statStoreErr: cacheErr}, nil, env.lf)

		posts, err := failRepo.GetPosts(ctx, []int64{2104})
		require.NoError(t, err, "统计回填失败只应降级，不影响结果")
		require.Len(t, posts, 1)
		assert.Equal(t, int64(1), posts[0].LikeCount)
	})

	t.Run("并发命中统计列经 flight 合并回源", func(t *testing.T) {
		const n = 8
		seedPost(t, env.db, 2205, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 9500)
		_, err := env.repo.GetPosts(ctx, []int64{2205}) // 首读回源：填 detail
		require.NoError(t, err)
		_, err = env.repo.GetPosts(ctx, []int64{2205}) // 二次命中：回填 stat
		require.NoError(t, err)
		require.NoError(t, env.db.Model(&models.PostDetail{}).Where("id = ?", 2205).Update("like_count", 42).Error)
		require.NoError(t, env.ec.DelPostStats(ctx, []int64{2205})) // 模拟 stat 过期：并发命中全 miss

		var wg sync.WaitGroup
		results := make([][]*models.PostDetail, n)
		errs := make([]error, n)
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = env.repo.GetPosts(ctx, []int64{2205})
			}(i)
		}
		wg.Wait()

		for i := range n {
			require.NoError(t, errs[i])
			require.Len(t, results[i], 1)
			assert.Equal(t, int64(42), results[i][0].LikeCount)
			assert.Equal(t, results[0][0], results[i][0], "并发合并回源应得到一致结果")
		}
		st := env.repo.bfStat.Stats()
		assert.GreaterOrEqual(t, st.Batches, int64(1), "统计列 miss 必经由 bfStat 回源（非直连 DB）")
		assert.LessOrEqual(t, st.Batches, int64(n))
	})

	t.Run("并发相同回源结果一致", func(t *testing.T) {
		const n = 8
		var wg sync.WaitGroup
		results := make([][]*models.PostDetail, n)
		errs := make([]error, n)
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = env.repo.GetPosts(ctx, []int64{postID1, postID2})
			}(i)
		}
		wg.Wait()

		for i := range n {
			require.NoError(t, errs[i])
			require.Len(t, results[i], 2)
			assert.Equal(t, results[0], results[i], "并发合并回源应得到一致结果")
		}
	})
}

// ---------- GetPostStat 直通 ----------

func TestCachedGetPostStat_直通不触缓存(t *testing.T) {
	env := newCachedPostRepoEnv(t)
	ctx := context.Background()

	seedPost(t, env.db, postID1, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 1000)

	gets, sets, dels := env.tc.Gets, env.tc.Sets, env.tc.Dels
	stats, err := env.repo.GetPostStat(ctx, []int64{postID1, 999999})
	require.NoError(t, err)
	require.Len(t, stats, 1, "不存在的 id 不返回")

	assert.Equal(t, postID1, stats[0].ID)
	assert.Equal(t, int64(1), stats[0].LikeCount)
	assert.Equal(t, int64(2), stats[0].FavouriteCount)
	assert.Equal(t, int64(3), stats[0].CommentCount)
	assert.Equal(t, int64(4), stats[0].ViewCount)

	assert.Equal(t, gets, env.tc.Gets, "统计直通不应读缓存")
	assert.Equal(t, sets, env.tc.Sets, "统计直通不应写缓存")
	assert.Equal(t, dels, env.tc.Dels, "统计直通不应删缓存")
}

// ---------- 写路径（失效登记） ----------

func TestCachedPostRepo_写后失效(t *testing.T) {
	env := newCachedPostRepoEnv(t)
	ctx := context.Background()

	t.Run("CreatePost 后清除空哨兵", func(t *testing.T) {
		// 先查询不存在的 id，立起空哨兵
		posts, err := env.repo.GetPosts(ctx, []int64{2101})
		require.NoError(t, err)
		assert.Empty(t, posts)

		err = env.repo.CreatePost(ctx, &models.PostDetail{
			BaseModel: common.BaseModel{ID: 2101},
			AuthorID:  authorID1,
			Title:     "新标题",
			FullText:  "新正文",
			Status:    postv1.PostStatus_POST_STATUS_DRAFT,
			MetaData:  datatypes.JSONMap{"theme": "dark"},
		})
		require.NoError(t, err)

		posts, err = env.repo.GetPosts(ctx, []int64{2101})
		require.NoError(t, err)
		require.Len(t, posts, 1, "哨兵/旧值应在创建后失效")
		assert.Equal(t, "新标题", posts[0].Title)
	})

	t.Run("UpdateDraftTitle 后失效", func(t *testing.T) {
		seedPost(t, env.db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, 1000)
		_, err := env.repo.GetPosts(ctx, []int64{postID1}) // 预填缓存
		require.NoError(t, err)

		info, err := env.repo.UpdateDraftTitle(ctx, postID1, "新标题")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		posts, err := env.repo.GetPosts(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "新标题", posts[0].Title, "更新后不应读到缓存旧值")
	})

	t.Run("UpdateDraftText 后失效", func(t *testing.T) {
		_, err := env.repo.GetPosts(ctx, []int64{postID1}) // 重新预填缓存
		require.NoError(t, err)

		info, err := env.repo.UpdateDraftText(ctx, postID1, "新正文")
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		posts, err := env.repo.GetPosts(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "新正文", posts[0].FullText)
	})

	t.Run("UpdateDraftMetaData 后失效", func(t *testing.T) {
		_, err := env.repo.GetPosts(ctx, []int64{postID1}) // 重新预填缓存
		require.NoError(t, err)

		info, err := env.repo.UpdateDraftMetaData(ctx, postID1, datatypes.JSONMap{"theme": "light"})
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		posts, err := env.repo.GetPosts(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, datatypes.JSONMap{"theme": "light"}, posts[0].MetaData)
	})

	t.Run("媒体关联变更后失效", func(t *testing.T) {
		seedPost(t, env.db, postID2, authorID2, postv1.PostStatus_POST_STATUS_PUBLISHED, 2000)
		seedPostMedia(t, env.db, 8001, postID2, 7001, 7101, 1)
		_, err := env.repo.GetPosts(ctx, []int64{postID2}) // 预填缓存（含 1 条媒体）
		require.NoError(t, err)

		// 删关联 → 失效 → 可见 0 条
		_, err = env.repo.DeletePostMedias(ctx, postID2)
		require.NoError(t, err)
		posts, err := env.repo.GetPosts(ctx, []int64{postID2})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Empty(t, posts[0].Medias, "删媒体后不应读到缓存旧关联")

		// 建关联 → 失效 → 可见新关联
		require.NoError(t, env.repo.CreatePostMedias(ctx, postID2, []*media.MediaFile{newMediaFile(7021, 7121)}))
		posts, err = env.repo.GetPosts(ctx, []int64{postID2})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		require.Len(t, posts[0].Medias, 1, "加媒体后不应读到缓存旧关联")
		assert.Equal(t, int64(7021), posts[0].Medias[0].Media.ID)
	})

	t.Run("SetPostStatus 后失效", func(t *testing.T) {
		seedPost(t, env.db, postID3, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, 3000)
		_, err := env.repo.GetPosts(ctx, []int64{postID3}) // 预填缓存
		require.NoError(t, err)

		info, err := env.repo.SetPostStatus(ctx, postID3, postv1.PostStatus_POST_STATUS_DRAFT, postv1.PostStatus_POST_STATUS_PUBLISHED)
		require.NoError(t, err)
		assert.Equal(t, int64(1), info.RowsAffected)

		posts, err := env.repo.GetPosts(ctx, []int64{postID3})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, posts[0].Status, "发布后不应读到缓存旧状态")
	})

	t.Run("SetPostBanned/SetPostDeleted 后失效", func(t *testing.T) {
		seedPost(t, env.db, postID4, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 4000)
		_, err := env.repo.GetPosts(ctx, []int64{postID4}) // 预填缓存
		require.NoError(t, err)

		_, err = env.repo.SetPostBanned(ctx, postID4)
		require.NoError(t, err)
		posts, err := env.repo.GetPosts(ctx, []int64{postID4})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_BANNED, posts[0].Status)

		_, err = env.repo.GetPosts(ctx, []int64{postID4}) // 重新预填缓存
		require.NoError(t, err)

		_, err = env.repo.SetPostDeleted(ctx, postID4)
		require.NoError(t, err)
		posts, err = env.repo.GetPosts(ctx, []int64{postID4})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, postv1.PostStatus_POST_STATUS_DELETED, posts[0].Status)
	})

	t.Run("DeletePost 物理删除后失效", func(t *testing.T) {
		seedPost(t, env.db, 2102, authorID1, postv1.PostStatus_POST_STATUS_DELETED, 5000)
		_, err := env.repo.GetPosts(ctx, []int64{2102}) // 预填缓存（DELETED 也入缓存）
		require.NoError(t, err)

		_, err = env.repo.DeletePost(ctx, authorID1, 2102)
		require.NoError(t, err)

		posts, err := env.repo.GetPosts(ctx, []int64{2102})
		require.NoError(t, err)
		assert.Empty(t, posts, "物理删除后不应读到缓存旧值")
	})

	t.Run("UpdatePost*Count 不触碰缓存且计数有界陈旧", func(t *testing.T) {
		const statPostID int64 = 2202
		seedPost(t, env.db, statPostID, authorID1, postv1.PostStatus_POST_STATUS_PUBLISHED, 2000)
		_, err := env.repo.GetPosts(ctx, []int64{statPostID}) // 首读回源：填 detail
		require.NoError(t, err)
		_, err = env.repo.GetPosts(ctx, []int64{statPostID}) // 二次命中：回填 stat（陈旧窗口成立的前提）
		require.NoError(t, err)

		gets, sets, dels := env.tc.Gets, env.tc.Sets, env.tc.Dels
		_, err = env.repo.UpdatePostLikeCount(ctx, statPostID, 100)
		require.NoError(t, err)
		_, err = env.repo.UpdatePostFavouriteCount(ctx, statPostID, 200)
		require.NoError(t, err)
		_, err = env.repo.UpdatePostCommentCount(ctx, statPostID, 300)
		require.NoError(t, err)
		_, err = env.repo.UpdatePostViewCount(ctx, statPostID, 400)
		require.NoError(t, err)
		assert.Equal(t, gets, env.tc.Gets)
		assert.Equal(t, sets, env.tc.Sets)
		assert.Equal(t, dels, env.tc.Dels, "计数更新不应触碰缓存（写路径零失效）")

		posts, err := env.repo.GetPosts(ctx, []int64{statPostID})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "标题", posts[0].Title, "主体仍命中缓存")
		assert.Equal(t, int64(1), posts[0].LikeCount, "TTL 内计数陈旧（写路径不失效，陈旧 ≤ stat_ttl）")
		assert.Equal(t, int64(2), posts[0].FavouriteCount)
		assert.Equal(t, int64(3), posts[0].CommentCount)
		assert.Equal(t, int64(4), posts[0].ViewCount)

		require.NoError(t, env.ec.DelPostStats(ctx, []int64{statPostID})) // 模拟 TTL 过期
		posts, err = env.repo.GetPosts(ctx, []int64{statPostID})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, int64(100), posts[0].LikeCount, "过期后 miss 轻查刷新为新计数")
		assert.Equal(t, int64(200), posts[0].FavouriteCount)
		assert.Equal(t, int64(300), posts[0].CommentCount)
		assert.Equal(t, int64(400), posts[0].ViewCount)
	})

	t.Run("失效失败不阻断业务（降级靠 TTL 兜底）", func(t *testing.T) {
		cacheErr := errors.New("cache down")
		failRepo := NewCachedPostRepo(env.inner, &postErrEntityCache{PostEntityCache: env.ec, delErr: cacheErr}, nil, env.lf)

		info, err := failRepo.UpdateDraftTitle(ctx, postID1, "降级标题")
		require.NoError(t, err, "失效失败不应失败业务写入")
		assert.Equal(t, int64(1), info.RowsAffected)
	})
}

// ---------- 事务模式 ----------

func TestCachedWithTX_事务直穿不缓存(t *testing.T) {
	env := newCachedPostRepoEnv(t)
	ctx := context.Background()

	t.Run("事务内读直穿且见自身写入，不触缓存", func(t *testing.T) {
		gets, sets := env.tc.Gets, env.tc.Sets
		err := env.db.Transaction(func(tx *gorm.DB) error {
			txRepo := env.repo.WithTX(tx)
			err := txRepo.CreatePost(ctx, &models.PostDetail{
				BaseModel: common.BaseModel{ID: 2101},
				AuthorID:  authorID1,
				Title:     "tx-标题",
				Status:    postv1.PostStatus_POST_STATUS_DRAFT,
			})
			require.NoError(t, err)

			posts, err := txRepo.GetPosts(ctx, []int64{2101})
			require.NoError(t, err)
			require.Len(t, posts, 1, "事务内须读到自身未提交的写入")
			assert.Equal(t, "tx-标题", posts[0].Title)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, gets, env.tc.Gets, "事务读不应读缓存")
		assert.Equal(t, sets, env.tc.Sets, "事务读不应写缓存")
	})

	t.Run("事务内写登记失效，提交后读到新值", func(t *testing.T) {
		seedPost(t, env.db, postID1, authorID1, postv1.PostStatus_POST_STATUS_DRAFT, 1000)
		_, err := env.repo.GetPosts(ctx, []int64{postID1}) // 预填缓存
		require.NoError(t, err)

		dels := env.tc.Dels
		err = env.db.Transaction(func(tx *gorm.DB) error {
			txRepo := env.repo.WithTX(tx)
			_, err := txRepo.UpdateDraftTitle(ctx, postID1, "tx-新标题")
			return err
		})
		require.NoError(t, err)
		assert.Greater(t, env.tc.Dels, dels, "事务内写应登记失效")

		posts, err := env.repo.GetPosts(ctx, []int64{postID1})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "tx-新标题", posts[0].Title)
	})

	t.Run("Transaction 包装：fn 拿事务绑定装饰器不失效旁路", func(t *testing.T) {
		seedPost(t, env.db, postID2, authorID2, postv1.PostStatus_POST_STATUS_DRAFT, 2000)
		_, err := env.repo.GetPosts(ctx, []int64{postID2}) // 预填缓存
		require.NoError(t, err)

		gets, sets, dels := env.tc.Gets, env.tc.Sets, env.tc.Dels
		err = env.repo.Transaction(func(repo PostRepository) error {
			if _, err := repo.UpdateDraftTitle(ctx, postID2, "tx-新标题"); err != nil {
				return err
			}
			// 事务内读直穿：见自身未提交写入，且不触缓存
			posts, err := repo.GetPosts(ctx, []int64{postID2})
			require.NoError(t, err)
			require.Len(t, posts, 1)
			assert.Equal(t, "tx-新标题", posts[0].Title)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, gets, env.tc.Gets, "fn 内读不应读缓存")
		assert.Equal(t, sets, env.tc.Sets, "fn 内读不应写缓存")
		assert.Greater(t, env.tc.Dels, dels, "fn 内写必须经装饰器登记失效（否则失效旁路）")

		posts, err := env.repo.GetPosts(ctx, []int64{postID2})
		require.NoError(t, err)
		require.Len(t, posts, 1)
		assert.Equal(t, "tx-新标题", posts[0].Title, "提交后不应读到缓存旧值")
	})
}

// ---------- 构造校验 ----------

func TestNewCachedPostRepo_依赖校验(t *testing.T) {
	require.Panics(t, func() { NewCachedPostRepo(nil, nil, nil, nil) }, "inner 缺失应 fail-fast")
	require.Panics(t, func() { NewCachedPostRepo(&PostRepoImpl{}, nil, nil, nil) }, "cache 缺失应 fail-fast")
}
