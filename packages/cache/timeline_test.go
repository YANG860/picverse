package cache_test

import (
	"context"
	"testing"

	"rednote/config"
	"rednote/packages/cache"
	"rednote/test/testredis"

	"github.com/stretchr/testify/require"
)

// newTimelineCapacity 自定义容量的发件箱缓存（默认 200 条用例中过大，测试用小容量）
func newTimelineCapacity(c cache.Cache, capacity int) cache.AuthorTimelineCache {
	return cache.NewAuthorTimelineCache(c, &config.Config{Cache: config.CacheConfig{TimelineCapacity: capacity}}, testLF{})
}

// TestAuthorTimelineAppendWindow 发布追加 + 游标取窗：
// (created_at, post_id) 倒序、严格早于游标（同毫秒按 post_id 元组序）、缓存见底 ≠ 未缓存
func TestAuthorTimelineAppendWindow(t *testing.T) {
	a := newTimelineCapacity(testredis.New(), 200)
	ctx := context.Background()

	// (created_at, post_id)：(100,1)(100,2)(200,3)——同毫秒两条验证元组序
	require.NoError(t, a.Append(ctx, 2001, 1, 100))
	require.NoError(t, a.Append(ctx, 2001, 2, 100))
	require.NoError(t, a.Append(ctx, 2001, 3, 200))

	// 首页（游标零值）：(created_at, post_id) 倒序
	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{{PostID: 3, CreatedAt: 200}, {PostID: 2, CreatedAt: 100}, {PostID: 1, CreatedAt: 100}}, entries)

	// 游标 (100, 2)：严格早于 → 只剩 (100, 1)（同毫秒按 post_id 严格序）
	entries, miss, err = a.Window(ctx, 2001, 100, 2, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{{PostID: 1, CreatedAt: 100}}, entries)

	// 游标 (100, 1)：窗内无更早行 → 空但非 miss（key 在：缓存见底 ≠ 到底，调用方须补 DB）
	entries, miss, err = a.Window(ctx, 2001, 100, 1, 10)
	require.NoError(t, err)
	require.False(t, miss, "缓存见底 ≠ 未缓存")
	require.Empty(t, entries)
}

// TestAuthorTimelineWindowMiss 未缓存作者：miss=true → 分流到收件箱路（非 pull 对象，不回源）；
// Rebuild 后建 key 即转 pull 路可命中（分流契约：key 存在性 = pull 判据）
func TestAuthorTimelineWindowMiss(t *testing.T) {
	a := newTimelineCapacity(testredis.New(), 200)
	ctx := context.Background()

	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.True(t, miss, "未缓存 = 非 pull 对象，分流收件箱路")
	require.Empty(t, entries)

	require.NoError(t, a.Rebuild(ctx, 2001, []cache.TimelineEntry{{PostID: 9, CreatedAt: 900}, {PostID: 8, CreatedAt: 800}}))
	entries, miss, err = a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{{PostID: 9, CreatedAt: 900}, {PostID: 8, CreatedAt: 800}}, entries)

	// 空回源行不建 key（仍 miss；避免空 key 造成"已缓存"假象）
	require.NoError(t, a.Rebuild(ctx, 2002, nil))
	_, miss, err = a.Window(ctx, 2002, 0, 0, 10)
	require.NoError(t, err)
	require.True(t, miss)
}

// TestAuthorTimelineShortPage 短取契约：len < limit 即缓存不能完整作答（调用方须补 DB，短取不等于到底）
func TestAuthorTimelineShortPage(t *testing.T) {
	a := newTimelineCapacity(testredis.New(), 200)
	ctx := context.Background()

	require.NoError(t, a.Append(ctx, 2001, 1, 100))
	entries, miss, err := a.Window(ctx, 2001, 0, 0, 5)
	require.NoError(t, err)
	require.False(t, miss)
	require.Len(t, entries, 1, "返回 1 < limit 5：短取，调用方补 DB")
}

// TestAuthorTimelineTrim 截尾：只保最新 Capacity 条（旧帖自然淘汰出缓存，回源 DB 兜底）
func TestAuthorTimelineTrim(t *testing.T) {
	a := newTimelineCapacity(testredis.New(), 3)
	ctx := context.Background()

	for i := int64(1); i <= 5; i++ {
		require.NoError(t, a.Append(ctx, 2001, i, i*100))
	}

	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{
		{PostID: 5, CreatedAt: 500}, {PostID: 4, CreatedAt: 400}, {PostID: 3, CreatedAt: 300},
	}, entries, "只保最新 3 条")
}

// TestAuthorTimelineRebuildAdditive 回填增量并入（不 DEL）：与并发 Append 天然合并、截尾保新
func TestAuthorTimelineRebuildAdditive(t *testing.T) {
	a := newTimelineCapacity(testredis.New(), 3)
	ctx := context.Background()

	require.NoError(t, a.Rebuild(ctx, 2001, []cache.TimelineEntry{{PostID: 2, CreatedAt: 200}, {PostID: 1, CreatedAt: 100}}))
	// 回源窗口与并发发布竞争：发布先落缓存、回填后到 → 不得覆盖/清掉
	require.NoError(t, a.Append(ctx, 2001, 3, 300))
	require.NoError(t, a.Rebuild(ctx, 2001, []cache.TimelineEntry{{PostID: 2, CreatedAt: 200}, {PostID: 1, CreatedAt: 100}}))

	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{
		{PostID: 3, CreatedAt: 300}, {PostID: 2, CreatedAt: 200}, {PostID: 1, CreatedAt: 100},
	}, entries, "增量并入：并发追加幸存")
}

// TestAuthorTimelineNoop 无缓存降级：Noop 底层恒 miss（行为等同无缓存，调用方回源 DB）
func TestAuthorTimelineNoop(t *testing.T) {
	a := newTimelineCapacity(cache.NewNoop(), 200)
	ctx := context.Background()

	require.NoError(t, a.Append(ctx, 2001, 1, 100))
	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.True(t, miss)
	require.Empty(t, entries)
}
