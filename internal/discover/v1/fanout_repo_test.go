package v1

import (
	"context"
	"testing"
	"time"

	models "rednote/internal/discover/models"
	"rednote/test/testdb"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newFanoutRepo(t *testing.T) (FanoutRepo, *gorm.DB) {
	db := testdb.Setup(t)
	return NewFanoutRepo(db), db
}

// fanoutCount 统计收件箱行数（uid 传 0 = 全部）
func fanoutCount(t *testing.T, db *gorm.DB, uid int64) int64 {
	t.Helper()
	q := db.Model(&models.FanoutTimeline{})
	if uid != 0 {
		q = q.Where("uid = ?", uid)
	}
	var n int64
	require.NoError(t, q.Count(&n).Error)
	return n
}

// TestFanoutWriteInboxIdempotent 扇出幂等：重投不重复行；空粉丝列表 no-op
func TestFanoutWriteInboxIdempotent(t *testing.T) {
	repo, db := newFanoutRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.WriteInbox(ctx, 1, 500, 100, []int64{11, 12}))
	require.NoError(t, repo.WriteInbox(ctx, 1, 500, 100, []int64{11, 12}), "重投须幂等")
	require.Equal(t, int64(2), fanoutCount(t, db, 0), "重投后仍为一人一行")

	require.NoError(t, repo.WriteInbox(ctx, 1, 500, 100, []int64{13}), "增量扇出")
	require.Equal(t, int64(3), fanoutCount(t, db, 0))

	require.NoError(t, repo.WriteInbox(ctx, 2, 500, 200, nil), "空粉丝列表 no-op")
	require.Equal(t, int64(3), fanoutCount(t, db, 0))
}

// TestFanoutReadInboxKeyset keyset 分页：(post_created_at, post_id) 倒序、严格早于游标、无重无漏
func TestFanoutReadInboxKeyset(t *testing.T) {
	repo, _ := newFanoutRepo(t)
	ctx := context.Background()

	// uid=1 五条：(ts, post_id) = (100,1) (100,2) (200,3) (200,4) (300,5)；uid=2 一条干扰
	for _, p := range []struct {
		postID, ts int64
	}{{1, 100}, {2, 100}, {3, 200}, {4, 200}, {5, 300}} {
		require.NoError(t, repo.WriteInbox(ctx, p.postID, 500, p.ts, []int64{1}))
	}
	require.NoError(t, repo.WriteInbox(ctx, 6, 500, 999, []int64{2}))

	type cursor struct{ ts, id int64 }
	want := []cursor{{300, 5}, {200, 4}, {200, 3}, {100, 2}, {100, 1}}

	cur := cursor{} // 首页
	var got []cursor
	for {
		rows, err := repo.ReadInbox(ctx, 1, cur.ts, cur.id, 2)
		require.NoError(t, err)
		if len(rows) == 0 {
			break
		}
		require.LessOrEqual(t, len(rows), 2)
		for _, r := range rows {
			got = append(got, cursor{r.PostCreatedAt, r.PostID})
		}
		last := rows[len(rows)-1]
		cur = cursor{last.PostCreatedAt, last.PostID}
	}
	require.Equal(t, want, got, "倒序翻页无重无漏")
}

// TestFanoutReadInboxEmpty 未知 uid 收件箱为空
func TestFanoutReadInboxEmpty(t *testing.T) {
	repo, _ := newFanoutRepo(t)
	rows, err := repo.ReadInbox(context.Background(), 99999, 0, 0, 10)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// TestFanoutNoCascadeDelete 不做删帖级联删除：已删帖行保留收件箱，由下游（BFF 回查组装过滤）
// 与前端自行过滤，行随保留期清理回收——本用例锁定"不提供按帖删行入口"的决策
func TestFanoutNoCascadeDelete(t *testing.T) {
	repo, db := newFanoutRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.WriteInbox(ctx, 1, 500, 100, []int64{11, 12}))
	require.NoError(t, repo.WriteInbox(ctx, 2, 500, 200, []int64{11}))

	// 帖1已删（下游过滤），收件箱行原样保留
	require.Equal(t, int64(3), fanoutCount(t, db, 0))
	rows, err := repo.ReadInbox(ctx, 11, 0, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

// TestFanoutCleanupExpired 保留期清理：只删 post_created_at 严格早于 cutoff 的行，
// 批删循环走完全部批次（batch=2 强制多批）；无过期行为 no-op
func TestFanoutCleanupExpired(t *testing.T) {
	repo, db := newFanoutRepo(t)
	ctx := context.Background()

	// 3 条过期（72h/48h 前），2 条在保留期内（现在）
	require.NoError(t, repo.WriteInbox(ctx, 1, 500, time.Now().Add(-72*time.Hour).UnixMilli(), []int64{11, 12}))
	require.NoError(t, repo.WriteInbox(ctx, 2, 500, time.Now().Add(-48*time.Hour).UnixMilli(), []int64{11}))
	require.NoError(t, repo.WriteInbox(ctx, 3, 500, time.Now().UnixMilli(), []int64{11, 12}))

	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	deleted, err := repo.CleanupExpired(ctx, cutoff, 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), deleted, "过期行全部删除（跨多批）")
	require.Equal(t, int64(2), fanoutCount(t, db, 0), "保留期内的行不动")

	deleted, err = repo.CleanupExpired(ctx, cutoff, 2)
	require.NoError(t, err)
	require.Equal(t, int64(0), deleted, "无过期行 no-op")
}
