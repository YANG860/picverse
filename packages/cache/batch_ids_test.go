package cache_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"rednote/packages/cache"
)

type splitEntity struct{ ID int64 }

func TestDedupeIDs_保序去重(t *testing.T) {
	assert.Equal(t, []int64{3, 1, 2}, cache.DedupeIDs([]int64{3, 1, 3, 2, 1}))
	assert.Empty(t, cache.DedupeIDs(nil))
	assert.Equal(t, []int64{7}, cache.DedupeIDs([]int64{7, 7, 7}))
}

func TestSplitHits_三分归类_保序且哨兵不入集合(t *testing.T) {
	hits := map[int64]*splitEntity{1: {ID: 1}, 3: {ID: 3}, 5: nil} // 5 = 空哨兵
	byID, hitIDs, missIDs := cache.SplitHits([]int64{1, 2, 3, 5, 4}, hits)

	assert.Len(t, byID, 2)
	assert.Equal(t, int64(1), byID[1].ID)
	assert.Equal(t, int64(3), byID[3].ID)
	assert.Equal(t, []int64{1, 3}, hitIDs, "命中 id 保持输入序")
	assert.Equal(t, []int64{2, 4}, missIDs, "未命中 id 保持输入序")
	_, sentinelCounted := byID[5]
	assert.False(t, sentinelCounted, "空哨兵不进命中集合")

	t.Run("全 miss（缓存不可用降级）", func(t *testing.T) {
		byID, hitIDs, missIDs := cache.SplitHits[splitEntity]([]int64{8, 9}, nil)
		assert.NotNil(t, byID, "降级路径也返回可写 map（调用方会 maps.Copy 进去）")
		assert.Empty(t, byID)
		assert.Empty(t, hitIDs)
		assert.Equal(t, []int64{8, 9}, missIDs)
	})
}
