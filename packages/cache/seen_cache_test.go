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

// newSeen 小窗口账本（过期滚动用例）
func newSeen(ttl time.Duration) *cache.SeenCache {
	return cache.NewSeenCache(testredis.New(), &config.Config{
		Recommend: config.RecommendConfig{SeenTTL: ttl},
	})
}

// TestSeenFilterMark 记账与过滤：等长对齐、未曝光全 false、记账后命中
func TestSeenFilterMark(t *testing.T) {
	s := newSeen(30 * 24 * time.Hour)
	ctx := context.Background()

	// 空账本：全未曝光
	seen, err := s.Filter(ctx, 1, []int64{1, 2, 3})
	require.NoError(t, err)
	assert.Equal(t, []bool{false, false, false}, seen)

	// 记账后命中（等长对齐）
	require.NoError(t, s.Mark(ctx, 1, []int64{2}))
	seen, err = s.Filter(ctx, 1, []int64{1, 2, 3})
	require.NoError(t, err)
	assert.Equal(t, []bool{false, true, false}, seen)

	// 用户隔离
	seen, err = s.Filter(ctx, 2, []int64{2})
	require.NoError(t, err)
	assert.Equal(t, []bool{false}, seen)

	// 空集 no-op
	seen, err = s.Filter(ctx, 1, nil)
	require.NoError(t, err)
	assert.Empty(t, seen)
	assert.NoError(t, s.Mark(ctx, 1, nil))
}

// TestSeenTrimWindow 去重窗口滚动：窗口外旧账出列（重复推荐窗口外内容可接受——已出热池窗口）
func TestSeenTrimWindow(t *testing.T) {
	s := newSeen(50 * time.Millisecond)
	ctx := context.Background()

	require.NoError(t, s.Mark(ctx, 1, []int64{1}))
	time.Sleep(60 * time.Millisecond)
	require.NoError(t, s.Mark(ctx, 1, []int64{2})) // 触发滚动清理（1 号出窗）

	seen, err := s.Filter(ctx, 1, []int64{1, 2})
	require.NoError(t, err)
	assert.Equal(t, []bool{false, true}, seen, "1 号出窗后可再次推荐")
}
