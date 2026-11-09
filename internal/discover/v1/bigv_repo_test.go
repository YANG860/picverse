package v1

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"rednote/config"
	models "rednote/internal/discover/models"
	"rednote/packages/defaults"
	"rednote/test/testdb"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newBigVRepo(t *testing.T, interval time.Duration) (*BigVRepo, *gorm.DB) {
	t.Helper()
	db := testdb.Setup(t)
	return NewBigVRepo(db, &config.Config{BigV: config.BigVConfig{SyncInterval: interval}}), db
}

// seedBigVRow 直插大V快照行（demotedAt 传 nil = 现任）
func seedBigVRow(t *testing.T, db *gorm.DB, uid int64, demotedAt *int64) {
	t.Helper()
	require.NoError(t, db.Create(&models.BigVAuthor{UID: uid, BecameBigVAt: 100, DemotedAt: demotedAt}).Error)
}

// TestBigVRepoLazySync 惰性同步：间隔内走内存快照（新落表行不可见），超过间隔一次读即回源
func TestBigVRepoLazySync(t *testing.T) {
	repo, db := newBigVRepo(t, 200*time.Millisecond)
	ctx := context.Background()
	seedBigVRow(t, db, 1, nil)

	// 首次读触发回源
	ok, err := repo.IsInPullSet(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)

	// 同步间隔内落表的新行不可见（走内存快照，≈ 滞后）
	seedBigVRow(t, db, 2, nil)
	ok, err = repo.IsInPullSet(ctx, 2)
	require.NoError(t, err)
	require.False(t, ok, "同步间隔内应走内存快照")

	// 超过间隔后的下一次读回源即可见
	time.Sleep(250 * time.Millisecond)
	ok, err = repo.IsInPullSet(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok, "超过同步间隔应回源")
}

// TestBigVRepoPullSetSemantics 名单口径：现任与宽限期降级者都在名单内（fanout 均跳过、pull 兜底）
func TestBigVRepoPullSetSemantics(t *testing.T) {
	repo, db := newBigVRepo(t, time.Hour)
	ctx := context.Background()

	demoted := int64(500)
	seedBigVRow(t, db, 1, nil)
	seedBigVRow(t, db, 2, &demoted)

	ok, err := repo.IsInPullSet(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok, "现任大V在名单内")

	ok, err = repo.IsInPullSet(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok, "宽限期降级者在名单内（宁重不漏）")

	ok, err = repo.IsInPullSet(ctx, 3)
	require.NoError(t, err)
	require.False(t, ok, "无行用户不在名单内")
}

// TestBigVRepoJoinPullSet 交集分流：命中保留输入顺序、未命中剔除
func TestBigVRepoJoinPullSet(t *testing.T) {
	repo, db := newBigVRepo(t, time.Hour)
	ctx := context.Background()

	demoted := int64(500)
	seedBigVRow(t, db, 3, &demoted)
	seedBigVRow(t, db, 1, nil)

	got, err := repo.JoinPullSet(ctx, []int64{3, 9, 1, 5})
	require.NoError(t, err)
	require.Equal(t, []int64{3, 1}, got, "命中交集且保持输入顺序")

	got, err = repo.JoinPullSet(ctx, []int64{7, 8})
	require.NoError(t, err)
	require.Empty(t, got, "全未命中返回空（走收件箱）")

	got, err = repo.JoinPullSet(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestBigVRepoEmpty 空表：判定为 false、交集为空
func TestBigVRepoEmpty(t *testing.T) {
	repo, _ := newBigVRepo(t, time.Hour)
	ctx := context.Background()

	ok, err := repo.IsInPullSet(ctx, 123)
	require.NoError(t, err)
	require.False(t, ok)

	got, err := repo.JoinPullSet(ctx, []int64{123})
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestBigVRepoDefaultInterval 配置为 0 回退默认同步间隔
func TestBigVRepoDefaultInterval(t *testing.T) {
	repo := NewBigVRepo(nil, &config.Config{})
	require.Equal(t, defaults.BigVSyncInterval, repo.interval)
}

// hideBigVTable 改名移走大V表模拟回源失败（表不存在 → query 报错），返回幂等恢复函数
func hideBigVTable(t *testing.T, db *gorm.DB) (restore func()) {
	t.Helper()
	require.NoError(t, db.Exec(`ALTER TABLE discover_bigv_authors RENAME TO discover_bigv_authors_bak`).Error)
	var restored bool
	restore = func() {
		if restored {
			return
		}
		restored = true
		require.NoError(t, db.Exec(`ALTER TABLE discover_bigv_authors_bak RENAME TO discover_bigv_authors`).Error)
	}
	t.Cleanup(func() { restore() })
	return restore
}

// TestBigVRepoLoadError 回源失败：IsInPullSet/JoinPullSet 均错误上抛（失败语义与直连一致），不吞错
func TestBigVRepoLoadError(t *testing.T) {
	repo, db := newBigVRepo(t, time.Hour)
	ctx := context.Background()
	hideBigVTable(t, db)

	ok, err := repo.IsInPullSet(ctx, 1)
	require.ErrorContains(t, err, "bigv repo load")
	require.False(t, ok)

	got, err := repo.JoinPullSet(ctx, []int64{1})
	require.ErrorContains(t, err, "bigv repo load")
	require.Empty(t, got)
}

// TestBigVRepoRetryAfterFailure 回源失败不推进同步点：间隔内下次读立即重试并成功
// （interval=1h：若失败错误推进了同步点，下次读将在间隔内持续返回旧值而不会重试）
func TestBigVRepoRetryAfterFailure(t *testing.T) {
	repo, db := newBigVRepo(t, time.Hour)
	ctx := context.Background()
	seedBigVRow(t, db, 7, nil)
	restore := hideBigVTable(t, db)

	_, err := repo.IsInPullSet(ctx, 7)
	require.ErrorContains(t, err, "bigv repo load")

	restore() // 表恢复后，间隔内下次读立即重试成功 = 同步点未被失败推进
	ok, err := repo.IsInPullSet(ctx, 7)
	require.NoError(t, err)
	require.True(t, ok)
}

// TestBigVRepoConcurrentReads 并发读（interval≈0 每次读都触发回源，覆盖回源互斥 + 双检 +
// 无锁快照读的重竞争路径）：结果全一致、无数据竞争（配合 -race）
func TestBigVRepoConcurrentReads(t *testing.T) {
	repo, db := newBigVRepo(t, time.Nanosecond)
	ctx := context.Background()
	demoted := int64(900)
	seedBigVRow(t, db, 1, nil)
	seedBigVRow(t, db, 2, &demoted)

	const workers, rounds = 8, 2
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			for range rounds {
				// 现任与宽限期降级者均在名单内
				for _, uid := range []int64{1, 2} {
					ok, err := repo.IsInPullSet(ctx, uid)
					if err != nil || !ok {
						errCh <- fmt.Errorf("IsInPullSet(%d) = %v, %w", uid, ok, err)
						return
					}
				}
				ok, err := repo.IsInPullSet(ctx, 3)
				if err != nil || ok {
					errCh <- fmt.Errorf("IsInPullSet(3) = %v, %w", ok, err)
					return
				}
				got, err := repo.JoinPullSet(ctx, []int64{2, 3, 1})
				if err != nil {
					errCh <- fmt.Errorf("JoinPullSet: %w", err)
					return
				}
				if len(got) != 2 || got[0] != 2 || got[1] != 1 {
					errCh <- fmt.Errorf("JoinPullSet 期望 [2 1]，得到 %v", got)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}
