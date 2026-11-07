package cache_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"log/slog"

	"rednote/config"
	postv1 "rednote/gen/go/post/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"
	post "rednote/internal/post/models"
	"rednote/packages/cache"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// testLF 日志工厂测试替身（丢弃式：断言不依赖日志输出）
type testLF struct{}

func (testLF) GetLoggerOf(string) *slog.Logger { return slog.Default() }
func (testLF) GetWriter() io.Writer            { return io.Discard }
func (testLF) Default() *slog.Logger           { return slog.Default() }
func (testLF) Close()                          {}

// newPostDetail 构造全字段帖子（统计列非零，验证入缓存时被清零；含 2 条媒体关联，
// 乱序插入验证 sort_order 往返不丢序）
func newPostDetail(id int64) *post.PostDetail {
	return &post.PostDetail{
		BaseModel:      common.BaseModel{ID: id, CreatedAt: 1700000000000, UpdatedAt: 1700000001000},
		AuthorID:       2001,
		Title:          "标题",
		FullText:       "正文",
		Status:         postv1.PostStatus_POST_STATUS_PUBLISHED,
		LikeCount:      1,
		FavouriteCount: 2,
		CommentCount:   3,
		ViewCount:      4,
		MetaData:       datatypes.JSONMap{"k": "v"},
		Medias: []*post.PostMedia{
			{
				BaseModel: common.BaseModel{ID: id*10 + 2, CreatedAt: 1700000000000, UpdatedAt: 1700000001000},
				PostID:    id,
				Media:     newCacheTestMedia(id*100 + 2),
				SortOrder: 2,
			},
			{
				BaseModel: common.BaseModel{ID: id*10 + 1, CreatedAt: 1700000000000, UpdatedAt: 1700000001000},
				PostID:    id,
				Media:     newCacheTestMedia(id*100 + 1),
				SortOrder: 1,
			},
		},
	}
}

// newCacheTestMedia 构造完整媒体文件（post_medias 嵌入列）
func newCacheTestMedia(id int64) *media.MediaFile {
	return &media.MediaFile{
		BaseModel:     common.BaseModel{ID: id, CreatedAt: 1700000000000, UpdatedAt: 1700000001000},
		AssetID:       id * 10,
		Type:          0,
		ObjKey:        "post/obj",
		Bucket:        "rednote",
		Sha256Sum:     "abc",
		SizeBytes:     1024,
		Format:        0,
		Height:        100,
		Width:         200,
		MediaMetaData: datatypes.JSONMap{"k": "v"},
	}
}

// ---------- 详情主体（非计数列） ----------

func TestStoreLoadPostDetail_全字段往返_统计列清零(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewPostEntityCache(tc, nil, testLF{})
	ctx := context.Background()
	detail := newPostDetail(2001)

	require.NoError(t, ec.StorePostDetail(ctx, detail.ID, detail))

	got, err := ec.LoadPostDetails(ctx, []int64{detail.ID})
	require.NoError(t, err)
	require.Contains(t, got, detail.ID)
	require.NotNil(t, got[detail.ID])

	want := *detail
	want.LikeCount = 0 // 只缓存非计数列
	want.FavouriteCount = 0
	want.CommentCount = 0
	want.ViewCount = 0
	assert.Equal(t, want, *got[detail.ID], "缓存往返应全字段一致且统计列清零")

	// 媒体关联完整往返且按 sort_order 顺序（数组保序）
	require.Len(t, got[detail.ID].Medias, 2)
	assert.Equal(t, detail.Medias[0], got[detail.ID].Medias[0], "媒体关联应完整往返且顺序不丢")
	assert.Equal(t, detail.Medias[1], got[detail.ID].Medias[1])

	// 入缓存的清零动作不得改动调用方实体
	assert.Equal(t, int64(1), detail.LikeCount)
	assert.Equal(t, int64(2), detail.FavouriteCount)
	assert.Equal(t, int64(3), detail.CommentCount)
	assert.Equal(t, int64(4), detail.ViewCount)
}

func TestStorePostDetail_Nil写空哨兵_与实体互相覆盖(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewPostEntityCache(tc, nil, testLF{})
	ctx := context.Background()
	const id int64 = 2001

	// 实体 → 空哨兵
	require.NoError(t, ec.StorePostDetail(ctx, id, newPostDetail(id)))
	require.NoError(t, ec.StorePostDetail(ctx, id, nil))
	got, err := ec.LoadPostDetails(ctx, []int64{id})
	require.NoError(t, err)
	require.Contains(t, got, id)
	assert.Nil(t, got[id], "空哨兵应映射为 nil")

	// 空哨兵 → 实体
	require.NoError(t, ec.StorePostDetail(ctx, id, newPostDetail(id)))
	got, err = ec.LoadPostDetails(ctx, []int64{id})
	require.NoError(t, err)
	require.NotNil(t, got[id])
	assert.Equal(t, "标题", got[id].Title)
}

func TestLoadPostDetails_Miss空入参与去重(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewPostEntityCache(tc, nil, testLF{})
	ctx := context.Background()

	t.Run("未缓存的 id 不在返回 map 中（miss）", func(t *testing.T) {
		got, err := ec.LoadPostDetails(ctx, []int64{999})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("空 ids 返回空 map 且不触缓存", func(t *testing.T) {
		gets := tc.Gets
		got, err := ec.LoadPostDetails(ctx, []int64{})
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Equal(t, gets, tc.Gets)
	})

	t.Run("重复 ids 返回单条", func(t *testing.T) {
		require.NoError(t, ec.StorePostDetail(ctx, 2001, newPostDetail(2001)))
		got, err := ec.LoadPostDetails(ctx, []int64{2001, 2001})
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})

	t.Run("id 参数为 key 依据（与实体内 ID 解耦）", func(t *testing.T) {
		require.NoError(t, ec.StorePostDetail(ctx, 55, newPostDetail(777)))
		got, err := ec.LoadPostDetails(ctx, []int64{55})
		require.NoError(t, err)
		require.Contains(t, got, int64(55))

		got, err = ec.LoadPostDetails(ctx, []int64{777})
		require.NoError(t, err)
		assert.NotContains(t, got, int64(777), "key 依据应为入参 id 而非实体 ID")
	})
}

// ---------- 统计列（就绪待启用） ----------

func TestStoreLoadPostStat_全字段往返与哨兵(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewPostEntityCache(tc, nil, testLF{})
	ctx := context.Background()
	stat := &post.PostStat{ID: 2001, LikeCount: 1, FavouriteCount: 2, CommentCount: 3, ViewCount: 4}

	require.NoError(t, ec.StorePostStat(ctx, stat.ID, stat))

	got, err := ec.LoadPostStats(ctx, []int64{stat.ID, 2002, 2003})
	require.NoError(t, err)
	require.Contains(t, got, stat.ID)
	assert.Equal(t, stat, got[stat.ID], "统计应全字段一致")
	assert.NotContains(t, got, int64(2002), "未缓存 id 应为 miss")
	assert.NotContains(t, got, int64(2003))

	require.NoError(t, ec.StorePostStat(ctx, 2002, nil))
	got, err = ec.LoadPostStats(ctx, []int64{2002})
	require.NoError(t, err)
	require.Contains(t, got, int64(2002))
	assert.Nil(t, got[2002], "空哨兵应映射为 nil")
}

// ---------- 失效（Del） ----------

func TestDelPost_失效独立且幂等(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewPostEntityCache(tc, nil, testLF{})
	ctx := context.Background()
	const id int64 = 2001

	require.NoError(t, ec.StorePostDetail(ctx, id, newPostDetail(id)))
	require.NoError(t, ec.StorePostStat(ctx, id, &post.PostStat{ID: id, LikeCount: 1}))

	require.NoError(t, ec.DelPostDetails(ctx, []int64{id}))
	got, err := ec.LoadPostDetails(ctx, []int64{id})
	require.NoError(t, err)
	assert.NotContains(t, got, id)

	stats, err := ec.LoadPostStats(ctx, []int64{id})
	require.NoError(t, err)
	assert.Contains(t, stats, id, "DelPostDetails 不应波及统计 key 空间")

	require.NoError(t, ec.DelPostStats(ctx, []int64{id}))
	stats, err = ec.LoadPostStats(ctx, []int64{id})
	require.NoError(t, err)
	assert.NotContains(t, stats, id)

	t.Run("重复删除幂等无错", func(t *testing.T) {
		require.NoError(t, ec.DelPostDetails(ctx, []int64{id}))
		require.NoError(t, ec.DelPostStats(ctx, []int64{id}))
	})

	t.Run("空 ids 不触缓存", func(t *testing.T) {
		dels := tc.Dels
		require.NoError(t, ec.DelPostDetails(ctx, []int64{}))
		require.NoError(t, ec.DelPostStats(ctx, []int64{}))
		assert.Equal(t, dels, tc.Dels)
	})
}

// ---------- 错误路径 ----------

func TestPostCache错误上抛_可降级(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("redis down")

	t.Run("批量读错误上抛", func(t *testing.T) {
		ec := cache.NewPostEntityCache(&flakyCache{Client: testredis.New(), mgetErr: sentinel}, nil, testLF{})
		_, err := ec.LoadPostDetails(ctx, []int64{2001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache post detail mget")

		_, err = ec.LoadPostStats(ctx, []int64{2001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache post stat mget")
	})

	t.Run("写错误上抛（实体与空哨兵）", func(t *testing.T) {
		ec := cache.NewPostEntityCache(&flakyCache{Client: testredis.New(), setErr: sentinel}, nil, testLF{})
		err := ec.StorePostDetail(ctx, 2001, newPostDetail(2001))
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache post detail set")

		err = ec.StorePostDetail(ctx, 2001, nil)
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "null sentinel")

		err = ec.StorePostStat(ctx, 2001, &post.PostStat{ID: 2001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache post stat set")
	})

	t.Run("删除错误上抛", func(t *testing.T) {
		ec := cache.NewPostEntityCache(&flakyCache{Client: testredis.New(), delErr: sentinel}, nil, testLF{})
		err := ec.DelPostDetails(ctx, []int64{2001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache post detail del")

		err = ec.DelPostStats(ctx, []int64{2001})
		require.ErrorIs(t, err, sentinel)
		assert.ErrorContains(t, err, "cache post stat del")
	})
}

func TestPost损坏值按错误上抛(t *testing.T) {
	ctx := context.Background()
	fc := &flakyCache{
		Client:  testredis.New(),
		corrupt: func(val []byte) []byte { return []byte("not-json") },
	}
	ec := cache.NewPostEntityCache(fc, nil, testLF{})

	require.NoError(t, ec.StorePostDetail(ctx, 2001, newPostDetail(2001)))
	_, err := ec.LoadPostDetails(ctx, []int64{2001})
	require.Error(t, err, "序列化损坏应上抛而非静默丢弃")
	assert.ErrorContains(t, err, "cache post detail unmarshal")

	require.NoError(t, ec.StorePostStat(ctx, 2001, &post.PostStat{ID: 2001}))
	_, err = ec.LoadPostStats(ctx, []int64{2001})
	require.Error(t, err)
	assert.ErrorContains(t, err, "cache post stat unmarshal")
}

// ---------- TTL ----------

func TestPostTTL_配置生效与默认回退(t *testing.T) {
	ctx := context.Background()

	t.Run("PostTTL 到期后按 miss 处理", func(t *testing.T) {
		ec := cache.NewPostEntityCache(testredis.New(), &config.Config{
			Cache: config.CacheConfig{PostTTL: 20 * time.Millisecond},
		}, testLF{})
		require.NoError(t, ec.StorePostDetail(ctx, 2001, newPostDetail(2001)))

		require.Eventually(t, func() bool {
			got, err := ec.LoadPostDetails(ctx, []int64{2001})
			return err == nil && len(got) == 0
		}, 2*time.Second, 5*time.Millisecond, "配置 TTL（含抖动）到期后应 miss")
	})

	t.Run("空哨兵按 NullTTL 到期", func(t *testing.T) {
		ec := cache.NewPostEntityCache(testredis.New(), &config.Config{
			Cache: config.CacheConfig{NullTTL: 20 * time.Millisecond},
		}, testLF{})
		require.NoError(t, ec.StorePostDetail(ctx, 2001, nil))

		require.Eventually(t, func() bool {
			got, err := ec.LoadPostDetails(ctx, []int64{2001})
			return err == nil && len(got) == 0
		}, 2*time.Second, 5*time.Millisecond, "空哨兵到期后应 miss")
	})

	t.Run("cfg 为 nil 时回退默认 TTL 且往返可用", func(t *testing.T) {
		ec := cache.NewPostEntityCache(testredis.New(), nil, testLF{})
		require.NoError(t, ec.StorePostDetail(ctx, 2001, newPostDetail(2001)))
		got, err := ec.LoadPostDetails(ctx, []int64{2001})
		require.NoError(t, err)
		assert.Contains(t, got, int64(2001))
	})
}

// ---------- 构造校验 ----------

func TestNewPostEntityCache_依赖校验(t *testing.T) {
	require.Panics(t, func() { cache.NewPostEntityCache(nil, nil, testLF{}) }, "Cache 缺失应 fail-fast")
	require.Panics(t, func() { cache.NewPostEntityCache(testredis.New(), nil, nil) }, "LoggerFactory 缺失应 fail-fast")
	require.NotPanics(t, func() { cache.NewPostEntityCache(testredis.New(), nil, testLF{}) }, "cfg 可为 nil（TTL 回退 defaults）")
}

// ==================== 浏览计数器（PV 近似，只丢不重） ====================

// TestPostViewCounter_IncrTake 取走：多次 +1 聚合取走、取走即清、脏 key/值丢弃、再取为空
func TestPostViewCounter_IncrTake(t *testing.T) {
	tc := testredis.New()
	ec := cache.NewPostEntityCache(tc, nil, testLF{})
	ctx := context.Background()

	// 计数累加（3 次 → delta 3）
	for range 3 {
		require.NoError(t, ec.IncrPostViewCount(ctx, 2001))
	}
	got, ok := tc.Raw("rn:v1:post:view:2001")
	require.True(t, ok, "计数器 key 应存在")
	assert.Equal(t, "3", string(got))

	// 结构性垃圾：非法 id key 与非法值（正常写入路径不存在）
	require.NoError(t, tc.Set(ctx, "rn:v1:post:view:abc", []byte("1"), 0))
	require.NoError(t, tc.Set(ctx, "rn:v1:post:view:2002", []byte("notnum"), 0))

	deltas, err := ec.TakePostViewDeltas(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{2001: 3}, deltas, "脏 key/值取走即弃，只留合法增量")

	// 取走即清（GETDEL 语义）
	_, ok = tc.Raw("rn:v1:post:view:2001")
	assert.False(t, ok, "取走后计数器应清空")

	again, err := ec.TakePostViewDeltas(ctx)
	require.NoError(t, err)
	assert.Empty(t, again, "无增量时返回空 map")
}

// TestPostViewCounter_错误上抛 IncrCounter 失败上抛由调用方降级（fail-open）
func TestPostViewCounter_错误上抛(t *testing.T) {
	sentinel := errors.New("redis down")
	ec := cache.NewPostEntityCache(&flakyCache{Client: testredis.New(), incrErr: sentinel}, nil, testLF{})

	err := ec.IncrPostViewCount(context.Background(), 2001)
	assert.ErrorIs(t, err, sentinel)
}
