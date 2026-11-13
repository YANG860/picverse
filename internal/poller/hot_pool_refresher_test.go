package poller

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"rednote/config"
	discovermodels "rednote/internal/discover/models"
	"rednote/packages/cache"
	"rednote/test/testredis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHotRepo HotRepo 测试替身（未覆写方法调用即 panic，未预期行为必须显式暴露）
type fakeHotRepo struct {
	cands  []discovermodels.HotCandidate
	cutoff int64
}

func (f *fakeHotRepo) ListHotCandidates(_ context.Context, cutoffMillis int64) ([]discovermodels.HotCandidate, error) {
	f.cutoff = cutoffMillis
	return f.cands, nil
}

// testLF poller 测试日志工厂
type testLF struct{}

func (testLF) GetLoggerOf(name any) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestRefresher 小参数维护器（测试可断言截尾/衰减）
func newTestRefresher(repo *fakeHotRepo, pool *cache.HotPoolCache, window time.Duration, hardCap int, gravity float64, w config.HotWeights) *HotPoolRefresher {
	return &HotPoolRefresher{
		repo:     repo,
		pool:     pool,
		interval: time.Hour,
		window:   window,
		hardCap:  hardCap,
		gravity:  gravity,
		weights:  w,
		log:      testLF{}.GetLoggerOf("test"),
	}
}

// TestHotScoreDecay 热度分：对数压缩 + 时间衰减（同 engagement 新帖分高）
func TestHotScoreDecay(t *testing.T) {
	pool := cache.NewHotPoolCache(testredis.New(), &config.Config{Hot: config.HotConfig{TTL: 30 * time.Minute}})
	now := time.Now()
	r := newTestRefresher(&fakeHotRepo{}, pool, time.Hour, 100, 1.5, config.HotWeights{Like: 1, Comment: 2})

	entries := r.score([]discovermodels.HotCandidate{
		{PostID: 1, LikeCount: 100, CreatedAt: now.Add(-46 * time.Hour).UnixMilli()}, // 老帖
		{PostID: 2, LikeCount: 100, CreatedAt: now.Add(-2 * time.Hour).UnixMilli()},  // 新帖
	}, now)

	require.Len(t, entries, 2)
	// score 按分数降序输出（热榜 TopN 口径）：新帖在前
	assert.Greater(t, entries[0].Score, entries[1].Score, "同 engagement 新帖分高（时间衰减）")
	// ln(1+100) / (2+2)^1.5 ≈ 4.615/8 = 0.577
	assert.InDelta(t, 0.577, entries[0].Score, 0.01)
}

// TestHotScoreFuse 保险丝：候选超 hardCap 按分数截尾（0 分先出）
func TestHotScoreFuse(t *testing.T) {
	pool := cache.NewHotPoolCache(testredis.New(), &config.Config{Hot: config.HotConfig{TTL: 30 * time.Minute}})
	now := time.Now()
	r := newTestRefresher(&fakeHotRepo{}, pool, time.Hour, 2, 1.5, config.HotWeights{Like: 1})

	entries := r.score([]discovermodels.HotCandidate{
		{PostID: 1, CreatedAt: now.UnixMilli()}, // 0 分新帖
		{PostID: 2, LikeCount: 100, CreatedAt: now.UnixMilli()},
		{PostID: 3, LikeCount: 50, CreatedAt: now.UnixMilli()},
	}, now)

	require.Len(t, entries, 2)
	assert.Equal(t, int64(2), entries[0].PostID)
	assert.Equal(t, int64(3), entries[1].PostID, "0 分新帖先出")
}

// TestHotPoolRefresherRefreshOnce 单轮重建：窗口候选 → 算分 → 池内容 + 淘汰（不重建即出池）
func TestHotPoolRefresherRefreshOnce(t *testing.T) {
	pool := cache.NewHotPoolCache(testredis.New(), &config.Config{Hot: config.HotConfig{TTL: 30 * time.Minute}})
	now := time.Now()
	repo := &fakeHotRepo{cands: []discovermodels.HotCandidate{
		{PostID: 1, LikeCount: 10, CreatedAt: now.Add(-time.Hour).UnixMilli()},
		{PostID: 2, AuthorID: 9, LikeCount: 100, CreatedAt: now.Add(-2 * time.Hour).UnixMilli()},
	}}
	r := newTestRefresher(repo, pool, 24*time.Hour, 100, 1.5, config.HotWeights{Like: 1})
	ctx := context.Background()

	r.refreshOnce(ctx)

	ms, err := pool.TopN(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, ms, 2)
	assert.Equal(t, int64(2), ms[0].PostID, "分高在前")
	assert.Greater(t, ms[0].Score, ms[1].Score)

	// 下轮候选只剩 2 号（1 号超窗/删帖）→ 不重建即淘汰
	repo.cands = repo.cands[1:]
	r.refreshOnce(ctx)
	ms, err = pool.TopN(ctx, 0, 10)
	require.NoError(t, err)
	assert.Len(t, ms, 1)
	assert.Equal(t, int64(2), ms[0].PostID)

	assert.InDelta(t, now.Add(-24*time.Hour).UnixMilli(), repo.cutoff, 1000, "cutoff = now - window")
}
