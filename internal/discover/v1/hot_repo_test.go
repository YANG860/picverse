package v1

import (
	"context"
	"testing"

	"rednote/test/testdb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// insertHotPost 直插 posts 行（status 为枚举数值：3=PUBLISHED）
func insertHotPost(t *testing.T, db *gorm.DB, id, createdAt int64, status int, likeCount int64) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO posts (id, created_at, status, like_count) VALUES (?, ?, ?, ?)",
		id, createdAt, status, likeCount,
	).Error)
}

// TestHotRepoListCandidates 候选查询过滤语义：仅 PUBLISHED（枚举值 3）+ 时间窗内，按 id 升序
// （回归防线：status 为 INTEGER 枚举列，字符串过滤曾为潜伏 bug——候选恒空）
func TestHotRepoListCandidates(t *testing.T) {
	db := testdb.Setup(t)
	repo := NewHotRepo(db)
	ctx := context.Background()
	const cutoff = int64(1000)

	insertHotPost(t, db, 1, 2000, 3, 10) // 窗口内 PUBLISHED ✓
	insertHotPost(t, db, 2, 500, 3, 10)  // 窗口外 ✗
	insertHotPost(t, db, 3, 2000, 1, 10) // DRAFT ✗
	insertHotPost(t, db, 4, 2000, 5, 10) // DELETED ✗
	insertHotPost(t, db, 5, 3000, 3, 20) // 窗口内 PUBLISHED ✓

	cands, err := repo.ListHotCandidates(ctx, cutoff)
	require.NoError(t, err)
	require.Len(t, cands, 2)

	assert.Equal(t, int64(1), cands[0].PostID, "按 id 升序")
	assert.Equal(t, int64(5), cands[1].PostID)
	assert.Equal(t, int64(10), cands[0].LikeCount)
	assert.Equal(t, int64(2000), cands[0].CreatedAt)
}

// TestHotRepoListCandidatesEmpty 无候选返回空（非 nil 语义由调用方裁决，这里只保证非错误）
func TestHotRepoListCandidatesEmpty(t *testing.T) {
	db := testdb.Setup(t)
	repo := NewHotRepo(db)

	cands, err := repo.ListHotCandidates(context.Background(), 1000)
	require.NoError(t, err)
	assert.Empty(t, cands)
}
