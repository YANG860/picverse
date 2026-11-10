package cache_test

import (
	"context"
	"testing"
	"time"

	"rednote/config"
	"rednote/packages/cache"
	"rednote/test/testredis"

	"github.com/stretchr/testify/require"
)

// newTimeline 自定义保险丝容量与截尾窗口的发件箱缓存（默认值用例中不合适，测试按需给小值）
func newTimeline(c cache.Cache, capacity int, retention time.Duration) cache.AuthorTimelineCache {
	return cache.NewAuthorTimelineCache(c, &config.Config{
		Cache:  config.CacheConfig{TimelineCapacity: capacity},
		Fanout: config.FanoutConfig{Retention: retention},
	}, testLF{})
}

// TestAuthorTimelineAppendWindow 发布追加 + 游标取窗：
// (created_at, post_id) 倒序、严格早于游标（同刻按 post_id 元组序）、缓存见底 ≠ 未缓存
func TestAuthorTimelineAppendWindow(t *testing.T) {
	a := newTimeline(testredis.New(), 5000, 30*24*time.Hour)
	ctx := context.Background()
	ts := time.Now().Unix()

	// (created_at, post_id)：(ts-100,1)(ts-100,2)(ts,3)——同刻两条验证元组序
	require.NoError(t, a.Append(ctx, 2001, 1, ts-100))
	require.NoError(t, a.Append(ctx, 2001, 2, ts-100))
	require.NoError(t, a.Append(ctx, 2001, 3, ts))

	// 首页（游标零值）：(created_at, post_id) 倒序
	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{
		{PostID: 3, CreatedAt: ts}, {PostID: 2, CreatedAt: ts - 100}, {PostID: 1, CreatedAt: ts - 100},
	}, entries)

	// 游标 (ts-100, 2)：严格早于 → 只剩 (ts-100, 1)（同刻按 post_id 严格序）
	entries, miss, err = a.Window(ctx, 2001, ts-100, 2, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{{PostID: 1, CreatedAt: ts - 100}}, entries)

	// 游标 (ts-100, 1)：窗内无更早行 → 空但非 miss（key 在：缓存见底 = 承诺窗口内见底，免回源）
	entries, miss, err = a.Window(ctx, 2001, ts-100, 1, 10)
	require.NoError(t, err)
	require.False(t, miss, "缓存见底 ≠ 未缓存")
	require.Empty(t, entries)
}

// TestAuthorTimelineWindowMiss 未缓存作者：miss=true → 分流到收件箱路（非 pull 对象，不回源）；
// Append 建 key 即转 pull 路可命中（分流契约：key 存在性 = pull 判据）
func TestAuthorTimelineWindowMiss(t *testing.T) {
	a := newTimeline(testredis.New(), 5000, 30*24*time.Hour)
	ctx := context.Background()
	ts := time.Now().Unix()

	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.True(t, miss, "未缓存 = 非 pull 对象，分流收件箱路")
	require.Empty(t, entries)

	require.NoError(t, a.Append(ctx, 2001, 9, ts-900))
	require.NoError(t, a.Append(ctx, 2001, 8, ts-800))
	entries, miss, err = a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{
		{PostID: 8, CreatedAt: ts - 800}, {PostID: 9, CreatedAt: ts - 900},
	}, entries)
}

// TestAuthorTimelineShortPage 短取契约：len < limit = 承诺窗口内真见底（时间截尾无截断），免回源
func TestAuthorTimelineShortPage(t *testing.T) {
	a := newTimeline(testredis.New(), 5000, 30*24*time.Hour)
	ctx := context.Background()
	ts := time.Now().Unix()

	require.NoError(t, a.Append(ctx, 2001, 1, ts-100))
	entries, miss, err := a.Window(ctx, 2001, 0, 0, 5)
	require.NoError(t, err)
	require.False(t, miss)
	require.Len(t, entries, 1, "返回 1 < limit 5：承诺窗口内见底，免回源")
}

// TestAuthorTimelineTrimByTime 时间截尾：承诺窗口（retention）外的 member 移除，
// 窗口内全量保留（缓存 = 承诺窗口全量，短取 ⇔ 见底的零回源依据）
func TestAuthorTimelineTrimByTime(t *testing.T) {
	a := newTimeline(testredis.New(), 5000, time.Hour) // 窗口 1h
	ctx := context.Background()
	now := time.Now().Unix()

	require.NoError(t, a.Append(ctx, 2001, 1, now-7200)) // 2h 前：窗口外，截尾即清
	require.NoError(t, a.Append(ctx, 2001, 2, now-60))
	require.NoError(t, a.Append(ctx, 2001, 3, now))

	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{
		{PostID: 3, CreatedAt: now}, {PostID: 2, CreatedAt: now - 60},
	}, entries, "窗口外 member 移除，窗口内全量（无条数截断）")
}

// TestAuthorTimelineFuse 保险丝：条数上限仅防事故刷帖（正常流量由时间截尾控制，永不触发）
func TestAuthorTimelineFuse(t *testing.T) {
	a := newTimeline(testredis.New(), 3, 30*24*time.Hour) // 保险丝 3，时间窗不触发
	ctx := context.Background()
	ts := time.Now().Unix()

	for i := int64(1); i <= 5; i++ {
		require.NoError(t, a.Append(ctx, 2001, i, ts-1000+i))
	}

	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, []cache.TimelineEntry{
		{PostID: 5, CreatedAt: ts - 995}, {PostID: 4, CreatedAt: ts - 996}, {PostID: 3, CreatedAt: ts - 997},
	}, entries, "事故态截尾保最新 3 条")
}

// TestAuthorTimelineNoop 无缓存降级：Noop 底层恒 miss（行为等同无缓存，调用方分流收件箱路）
func TestAuthorTimelineNoop(t *testing.T) {
	a := newTimeline(cache.NewNoop(), 200, 30*24*time.Hour)
	ctx := context.Background()

	require.NoError(t, a.Append(ctx, 2001, 1, time.Now().Unix()))
	entries, miss, err := a.Window(ctx, 2001, 0, 0, 10)
	require.NoError(t, err)
	require.True(t, miss)
	require.Empty(t, entries)
}

// TestAuthorTimelineBigVMask 防抖动标识：MarkBigV 设/续 TTL，IsBigV 判定；
// 大V标识 TTL 过期后自然回归 fanout（供给路径切换由调用方判定）
func TestAuthorTimelineBigVMask(t *testing.T) {
	a := newTimeline(testredis.New(), 5000, 30*24*time.Hour)
	ctx := context.Background()

	marked, err := a.IsBigV(ctx, 2001)
	require.NoError(t, err)
	require.False(t, marked, "初始无标识")

	require.NoError(t, a.MarkBigV(ctx, 2001))
	marked, err = a.IsBigV(ctx, 2001)
	require.NoError(t, err)
	require.True(t, marked)

	// 重复 Mark = 覆盖刷新 TTL（幂等）
	require.NoError(t, a.MarkBigV(ctx, 2001))
	marked, err = a.IsBigV(ctx, 2001)
	require.NoError(t, err)
	require.True(t, marked)

	// 未标识作者不受影响
	marked, err = a.IsBigV(ctx, 2002)
	require.NoError(t, err)
	require.False(t, marked)
}
