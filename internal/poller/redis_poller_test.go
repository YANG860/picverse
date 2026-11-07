package poller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"rednote/internal/post/v1"
	"rednote/packages/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gen"
)

// fakeViewCache 浏览计数器替身：TakePostViewDeltas 模拟 GETDEL 取走语义（取走即清空）
type fakeViewCache struct {
	cache.PostEntityCache
	deltas    map[int64]int64
	takeErr   error
	takeCalls int
}

func (f *fakeViewCache) TakePostViewDeltas(context.Context) (map[int64]int64, error) {
	f.takeCalls++
	if f.takeErr != nil {
		return nil, f.takeErr
	}
	d := f.deltas
	f.deltas = nil
	return d, nil
}

// viewWrite 一次写库记录
type viewWrite struct {
	postID, delta int64
}

// fakeViewRepo 写库替身：可注入错误、可模拟帖子不存在（RowsAffected=0）
type fakeViewRepo struct {
	v1.PostRepository

	mu      sync.Mutex
	calls   []viewWrite
	incErr  error
	missing map[int64]bool
}

func (f *fakeViewRepo) IncPostViewCount(_ context.Context, postID, delta int64) (gen.ResultInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, viewWrite{postID, delta})
	if f.incErr != nil {
		return gen.ResultInfo{}, f.incErr
	}
	if f.missing[postID] {
		return gen.ResultInfo{RowsAffected: 0}, nil
	}
	return gen.ResultInfo{RowsAffected: 1}, nil
}

func (f *fakeViewRepo) got() []viewWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]viewWrite(nil), f.calls...)
}

func newTestRedisPoller(repo v1.PostRepository, pc cache.PostEntityCache) *RedisPoller {
	return &RedisPoller{Repo: repo, Cache: pc, interval: time.Hour, log: slog.Default()}
}

// TestRedisPollerFlushOnce 刷回：取走增量逐帖写回；帖子不存在丢弃；取走失败保留待重试
func TestRedisPollerFlushOnce(t *testing.T) {
	t.Run("正常刷回：增量逐帖写回，不存在的帖子丢弃", func(t *testing.T) {
		repo := &fakeViewRepo{missing: map[int64]bool{999999: true}}
		pc := &fakeViewCache{deltas: map[int64]int64{2001: 3, 999999: 2}}
		p := newTestRedisPoller(repo, pc)

		p.flushOnce(context.Background())

		assert.ElementsMatch(t, []viewWrite{{2001, 3}, {999999, 2}}, repo.got())
		assert.Nil(t, pc.deltas, "取走即清空（GETDEL 语义）")
	})

	t.Run("取走失败：增量保留，下轮再试", func(t *testing.T) {
		repo := &fakeViewRepo{}
		pc := &fakeViewCache{deltas: map[int64]int64{2001: 3}, takeErr: errors.New("redis down")}
		p := newTestRedisPoller(repo, pc)

		p.flushOnce(context.Background())

		assert.Equal(t, 1, pc.takeCalls)
		assert.Equal(t, map[int64]int64{2001: 3}, pc.deltas, "未取走的增量不得丢失")
		assert.Empty(t, repo.got())
	})

	t.Run("写库失败：丢弃不回填（只丢不重）", func(t *testing.T) {
		repo := &fakeViewRepo{incErr: errors.New("db down")}
		pc := &fakeViewCache{deltas: map[int64]int64{2001: 3}}
		p := newTestRedisPoller(repo, pc)

		p.flushOnce(context.Background()) // 不 panic、不回填

		assert.Nil(t, pc.deltas, "已取走的增量写库失败即丢弃，不回填（回填有双计风险）")
	})
}

// TestRedisPollerRunRun 循环刷回并随 ctx 取消退出（退出前冲刷残留）
func TestRedisPollerRun(t *testing.T) {
	repo := &fakeViewRepo{}
	pc := &fakeViewCache{deltas: map[int64]int64{2001: 3}}
	p := &RedisPoller{Repo: repo, Cache: pc, interval: 5 * time.Millisecond, log: slog.Default()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return len(repo.got()) > 0
	}, time.Second, 5*time.Millisecond, "ticker 首轮应完成写回")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 Run 未退出")
	}

	assert.ElementsMatch(t, []viewWrite{{2001, 3}}, repo.got())
	assert.Nil(t, pc.deltas, "增量应已取走")
}
