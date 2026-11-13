package cache_test

import (
	"context"
	"testing"
	"time"

	"rednote/config"
	"rednote/packages/cache"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHotPool 小 TTL 热池（TTL 兜底语义用例）
func newHotPool(ttl time.Duration) *cache.HotPoolCache {
	return cache.NewHotPoolCache(testredis.New(), &config.Config{
		Hot: config.HotConfig{TTL: ttl},
	})
}

// TestHotPoolAddRebuild 入池两路：发帖钩子 0 分占位（不覆盖已算分）+ 维护重建（淘汰=不重建）；
// member 编码 post_id:author_id 往返（打散信息随池携带）
func TestHotPoolAddRebuild(t *testing.T) {
	p := newHotPool(30 * time.Minute)
	ctx := context.Background()

	// 发帖钩子：0 分占位入池
	require.NoError(t, p.Add(ctx, 1, 100))
	require.NoError(t, p.Add(ctx, 2, 100))
	ms, err := p.TopN(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, ms, 2)
	assert.Zero(t, ms[0].Score)

	// 维护重建：算分覆盖 + 出池（1 号不再候选 → 不重建即淘汰）
	require.NoError(t, p.Rebuild(ctx, []cache.HotEntry{
		{PostID: 2, AuthorID: 100, Score: 5}, {PostID: 3, AuthorID: 200, Score: 3},
	}))
	ms, err = p.TopN(ctx, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []cache.HotEntry{
		{PostID: 2, AuthorID: 100, Score: 5}, {PostID: 3, AuthorID: 200, Score: 3},
	}, ms)

	// 钩子幂等不覆盖：已有成员（2 号 5 分）不被 0 分清零，新成员（4 号）0 分占位
	require.NoError(t, p.Add(ctx, 2, 100))
	require.NoError(t, p.Add(ctx, 4, 300))
	ms, err = p.TopN(ctx, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []cache.HotEntry{
		{PostID: 2, AuthorID: 100, Score: 5}, {PostID: 3, AuthorID: 200, Score: 3}, {PostID: 4, AuthorID: 300, Score: 0},
	}, ms)
}

// TestHotPoolRebuildEmpty 空重建拒绝（空池会让热榜整体空窗，保留旧池）
func TestHotPoolRebuildEmpty(t *testing.T) {
	p := newHotPool(30 * time.Minute)
	ctx := context.Background()

	require.NoError(t, p.Rebuild(ctx, []cache.HotEntry{{PostID: 1, AuthorID: 1, Score: 1}}))
	require.Error(t, p.Rebuild(ctx, nil))
	ms, err := p.TopN(ctx, 0, 10)
	require.NoError(t, err)
	assert.Len(t, ms, 1, "空重建失败，旧池保留")
}

// TestHotPoolTopNPaging 榜单翻页：热度降序、offset 推进、短取即到底
func TestHotPoolTopNPaging(t *testing.T) {
	p := newHotPool(30 * time.Minute)
	ctx := context.Background()

	require.NoError(t, p.Rebuild(ctx, []cache.HotEntry{
		{PostID: 1, AuthorID: 1, Score: 1}, {PostID: 3, AuthorID: 1, Score: 3}, {PostID: 2, AuthorID: 2, Score: 2},
	}))

	// 首页降序
	ms, err := p.TopN(ctx, 0, 2)
	require.NoError(t, err)
	require.Len(t, ms, 2)
	assert.Equal(t, int64(3), ms[0].PostID)
	assert.Equal(t, int64(2), ms[1].PostID)

	// 次页 + 短取（末页 1 条 < limit，调用方据此判到底）
	ms, err = p.TopN(ctx, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, []cache.HotEntry{{PostID: 1, AuthorID: 1, Score: 1}}, ms)

	// 越界 = 到底
	ms, err = p.TopN(ctx, 3, 2)
	require.NoError(t, err)
	assert.Empty(t, ms)
}

// TestHotPoolSample 探索位随机采样（不放回；0 分新帖占位同样可采；author 随 member 携带）
func TestHotPoolSample(t *testing.T) {
	p := newHotPool(30 * time.Minute)
	ctx := context.Background()

	require.NoError(t, p.Add(ctx, 1, 100))
	require.NoError(t, p.Add(ctx, 2, 100))
	require.NoError(t, p.Add(ctx, 3, 200))

	ms, err := p.Sample(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, ms, 2)

	ms, err = p.Sample(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, ms, 3, "超量采样收敛为全量")
	for _, e := range ms {
		assert.NotZero(t, e.AuthorID, "author 随 member 携带")
	}
}

// TestHotPoolTTLMiss TTL 兜底：停摆无写入时池消亡（热榜降级空页 fail-soft）
func TestHotPoolTTLMiss(t *testing.T) {
	p := cache.NewHotPoolCache(testredis.New(), &config.Config{
		Hot: config.HotConfig{TTL: 10 * time.Millisecond},
	})
	ctx := context.Background()

	require.NoError(t, p.Add(ctx, 1, 1))
	time.Sleep(20 * time.Millisecond)

	ms, err := p.TopN(ctx, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, ms, "TTL 到期池消亡")
}
