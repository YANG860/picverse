package cache_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rednote/packages/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedFetch 门控取数器：记录每次调用的 keys；第 i 次调用进入时向 entered 发 i，
// 然后阻塞在 gates[i]（nil 或越界则不阻塞），放行后按键返回值（missing 中的键缺席）。
// 测试用它精确圈定批成员，不靠 sleep。
type gatedFetch struct {
	mu      sync.Mutex
	calls   [][]int
	callN   int
	gates   []chan struct{}
	entered chan int
	missing map[int]bool
	err     error
}

func newGatedFetch(gates ...chan struct{}) *gatedFetch {
	return &gatedFetch{
		gates:   gates,
		entered: make(chan int, 100),
		missing: map[int]bool{},
	}
}

func (g *gatedFetch) fetch(ctx context.Context, keys []int) (map[int]int, error) {
	g.mu.Lock()
	idx := g.callN
	g.callN++
	g.calls = append(g.calls, slices.Clone(keys))
	var gate chan struct{}
	if idx < len(g.gates) {
		gate = g.gates[idx]
	}
	g.mu.Unlock()

	g.entered <- idx

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if g.err != nil {
		return nil, g.err
	}
	out := make(map[int]int, len(keys))
	for _, k := range keys {
		if g.missing[k] {
			continue
		}
		out[k] = k * 10 // 值 = key*10，便于断言值归属正确
	}
	return out, nil
}

// callKeys 返回第 i 次调用的 keys（排序后）
func (g *gatedFetch) callKeys(i int) []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	ks := slices.Clone(g.calls[i])
	slices.Sort(ks)
	return ks
}

func (g *gatedFetch) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.callN
}

// waitFor 轮询等待条件成立（有界 2s，条件确定性成立故非 flaky）
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", msg)
}

func bg() context.Context {
	return context.Background()
}

// 相同 key 集合并：fetch 飞行期间到达的调用全部挂载，只回源一次
func TestBatchFlight_SameKeysCoalesce(t *testing.T) {
	g := newGatedFetch(make(chan struct{})) // fetch₁ 门控
	bf := cache.NewBatchFlight[int, int](2, 500, time.Second, g.fetch)
	defer bf.Close()

	var wg sync.WaitGroup
	results := make([]map[int]int, 11)
	errs := make([]error, 11)
	do := func(i int, keys []int) {
		defer wg.Done()
		results[i], errs[i] = bf.Do(bg(), keys)
	}

	wg.Add(1)
	go do(0, []int{1, 2})
	<-g.entered // fetch₁ 已进入（在途）

	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go do(i, []int{1, 2})
	}
	waitFor(t, func() bool { return bf.Stats().Joined >= 20 }, "10 个调用全部挂载")

	close(g.gates[0]) // 放行 fetch₁
	wg.Wait()

	require.Equal(t, 1, g.callCount(), "相同 key 并发只回源一次")
	for i := range results {
		require.NoError(t, errs[i])
		require.Equal(t, map[int]int{1: 10, 2: 20}, results[i])
	}
	st := bf.Stats()
	require.Equal(t, int64(1), st.Batches)
	require.Equal(t, int64(2), st.Fetched)
	require.Equal(t, int64(20), st.Joined)
}

// K=1 串行波（退回语义）：飞行期间登记的新 key 由下一批取到，重叠 key 不重复回源
func TestBatchFlight_WaveCoalesce(t *testing.T) {
	release1, release2 := make(chan struct{}), make(chan struct{})
	g := newGatedFetch(release1, release2)
	bf := cache.NewBatchFlight[int, int](1, 500, time.Second, g.fetch) // K=1：串行波
	defer bf.Close()

	var wg sync.WaitGroup
	var a, b map[int]int
	var aErr, bErr error
	wg.Add(2)
	go func() { defer wg.Done(); a, aErr = bf.Do(bg(), []int{1, 2}) }()
	<-g.entered // fetch₁={1,2} 在途
	go func() { defer wg.Done(); b, bErr = bf.Do(bg(), []int{2, 3}) }()
	waitFor(t, func() bool { return bf.Stats().Joined >= 1 }, "key2 已挂载（key3 同锁段已登记）")

	require.Equal(t, 1, g.callCount(), "K=1 串行波：fetch₂ 必须等 fetch₁ 放行后才起批")
	close(release1) // fetch₁ 放行 → worker 循环取下一批 {3}
	<-g.entered     // fetch₂ 已进入

	close(release2)
	wg.Wait()

	require.NoError(t, aErr)
	require.NoError(t, bErr)
	require.Equal(t, map[int]int{1: 10, 2: 20}, a)
	require.Equal(t, map[int]int{2: 20, 3: 30}, b)

	require.Equal(t, 2, g.callCount(), "两批：{1,2} 与 {3}")
	require.Equal(t, []int{1, 2}, g.callKeys(0))
	require.Equal(t, []int{3}, g.callKeys(1), "key2 只被回源一次，key3 免打扰下一批取到（无丢失唤醒）")
}

// K-worker 在途并发：fetch₁ 飞行中登记新 key → 立即起并发批（消除队头阻塞）
func TestBatchFlight_ConcurrentWorkers(t *testing.T) {
	release1, release2 := make(chan struct{}), make(chan struct{})
	g := newGatedFetch(release1, release2)
	bf := cache.NewBatchFlight[int, int](2, 500, time.Second, g.fetch)
	defer bf.Close()

	var wg sync.WaitGroup
	var a, b map[int]int
	var aErr, bErr error
	wg.Add(2)
	go func() { defer wg.Done(); a, aErr = bf.Do(bg(), []int{1, 2}) }()
	first := <-g.entered // fetch₁={1,2} 在途（门控未放行）
	go func() { defer wg.Done(); b, bErr = bf.Do(bg(), []int{2, 3}) }()
	second := <-g.entered // fetch₂ 在 fetch₁ 未放行时即进入 → 并发批成立

	require.Equal(t, 0, first)
	require.Equal(t, 1, second)
	require.Equal(t, []int{1, 2}, g.callKeys(first))
	require.Equal(t, []int{3}, g.callKeys(second), "key2 挂在 fetch₁ 的 call 上，key3 立即并发取，不受队头阻塞")
	require.Equal(t, int64(1), bf.Stats().Joined, "key2 被合并")

	close(release1)
	close(release2)
	wg.Wait()

	require.NoError(t, aErr)
	require.NoError(t, bErr)
	require.Equal(t, map[int]int{1: 10, 2: 20}, a)
	require.Equal(t, map[int]int{2: 20, 3: 30}, b)
}

// 批次上限：积压自动切分多批，每批 ≤ maxBatchSize，余量补唤醒不丢工作
func TestBatchFlight_MaxBatchSplits(t *testing.T) {
	g := newGatedFetch()
	bf := cache.NewBatchFlight[int, int](1, 2, time.Second, g.fetch) // K=1、每批至多 2
	defer bf.Close()

	out, err := bf.Do(bg(), []int{1, 2, 3, 4, 5})
	require.NoError(t, err)
	require.Equal(t, map[int]int{1: 10, 2: 20, 3: 30, 4: 40, 5: 50}, out)

	require.Equal(t, 3, g.callCount(), "5 key 按上限 2 切 3 批（2+2+1）")
	got := make(map[int]bool)
	for i := 0; i < g.callCount(); i++ {
		ks := g.callKeys(i)
		require.LessOrEqual(t, len(ks), 2, "每批 ≤ maxBatchSize")
		for _, k := range ks {
			require.False(t, got[k], "key %d 不跨批重复回源", k)
			got[k] = true
		}
	}
	require.Len(t, got, 5, "全量覆盖")
}

// SetConcurrency 运行时调参：K=1 → 2 立即解除排队（补 worker、抬名额），1 可随时退回串行波
func TestBatchFlight_SetConcurrency(t *testing.T) {
	release1, release2 := make(chan struct{}), make(chan struct{})
	g := newGatedFetch(release1, release2)
	bf := cache.NewBatchFlight[int, int](1, 500, time.Second, g.fetch)
	defer bf.Close()

	var wg sync.WaitGroup
	var a, b map[int]int
	var aErr, bErr error
	wg.Add(2)
	go func() { defer wg.Done(); a, aErr = bf.Do(bg(), []int{1}) }()
	<-g.entered // fetch₁ 在途（唯一 worker 占满槽）
	go func() { defer wg.Done(); b, bErr = bf.Do(bg(), []int{2}) }()

	require.Equal(t, 1, g.callCount(), "K=1：fetch₂ 排队等待")

	bf.SetConcurrency(2) // 抬上限 + 补 worker → 排队批立即起跑
	second := <-g.entered
	require.Equal(t, 1, second, "fetch₂ 在 fetch₁ 未放行时即进入")

	close(release1)
	close(release2)
	wg.Wait()

	require.NoError(t, aErr)
	require.NoError(t, bErr)
	require.Equal(t, map[int]int{1: 10}, a)
	require.Equal(t, map[int]int{2: 20}, b)
}

// 错误整批共享：批查询失败 → 该批所有等待者拿到同一 error，只回源一次
func TestBatchFlight_ErrorShared(t *testing.T) {
	g := newGatedFetch(make(chan struct{}))
	g.err = fmt.Errorf("db down")
	bf := cache.NewBatchFlight[int, int](2, 500, time.Second, g.fetch)
	defer bf.Close()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	do := func(i int) {
		defer wg.Done()
		_, errs[i] = bf.Do(bg(), []int{1, 2})
	}
	wg.Add(1)
	go do(0)
	<-g.entered
	wg.Add(1)
	go do(1)
	waitFor(t, func() bool { return bf.Stats().Joined >= 2 }, "第二个调用已挂载")

	close(g.gates[0])
	wg.Wait()

	require.Equal(t, 1, g.callCount())
	require.ErrorContains(t, errs[0], "db down")
	require.ErrorContains(t, errs[1], "db down")
}

// 缺 key = 确认不存在（非错误）：不在返回 map，调用方按空哨兵口径处理
func TestBatchFlight_MissingKeyNotExist(t *testing.T) {
	g := newGatedFetch()
	g.missing[2] = true
	bf := cache.NewBatchFlight[int, int](2, 500, time.Second, g.fetch)
	defer bf.Close()

	out, err := bf.Do(bg(), []int{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, map[int]int{1: 10, 3: 30}, out, "key2 确认不存在：缺席且无 error")
}

// 发起者取消不掀翻共享查询：A 取消返回 ctx 错误，B 照常拿到值
func TestBatchFlight_CtxCancelIsolation(t *testing.T) {
	g := newGatedFetch(make(chan struct{}))
	bf := cache.NewBatchFlight[int, int](2, 500, time.Second, g.fetch)
	defer bf.Close()

	ctxA, cancelA := context.WithCancel(bg())
	var aErr error
	done := make(chan struct{})
	go func() { defer close(done); _, aErr = bf.Do(ctxA, []int{1}) }()
	<-g.entered

	var b map[int]int
	var bErr error
	bDone := make(chan struct{})
	go func() { defer close(bDone); b, bErr = bf.Do(bg(), []int{1}) }()
	waitFor(t, func() bool { return bf.Stats().Joined >= 1 }, "B 已挂载")

	cancelA() // A 退出
	<-done
	require.ErrorIs(t, aErr, context.Canceled)

	close(g.gates[0]) // 共享 fetch 照常完成
	<-bDone
	require.NoError(t, bErr)
	require.Equal(t, map[int]int{1: 10}, b)

	// 无毒化：call 已 resolve 摘除，后续同 key Do 正常起新批
	out, err := bf.Do(bg(), []int{1})
	require.NoError(t, err)
	require.Equal(t, map[int]int{1: 10}, out)
	require.Equal(t, 2, g.callCount())
}

// fetch panic → 转整批错误，不死锁、不毒化（同一实例后续调用照常）
func TestBatchFlight_FetchPanic(t *testing.T) {
	var n atomic.Int32
	panicky := func(ctx context.Context, keys []int) (map[int]int, error) {
		if n.Add(1) == 1 {
			panic("boom") // 首次调用 panic，之后正常
		}
		out := make(map[int]int, len(keys))
		for _, k := range keys {
			out[k] = k * 10
		}
		return out, nil
	}
	bf := cache.NewBatchFlight[int, int](2, 500, time.Second, panicky)
	defer bf.Close()

	out, err := bf.Do(bg(), []int{1})
	require.ErrorContains(t, err, "fetch panic")
	require.Nil(t, out)

	// 无毒化：同一实例后续 Do 正常
	out, err = bf.Do(bg(), []int{1})
	require.NoError(t, err)
	require.Equal(t, map[int]int{1: 10}, out)
}

// 超时防毒化：fetch 挂死（不理会 ctx）也按期以错误 resolve，同一实例后续调用不被拖垮
func TestBatchFlight_TimeoutNoPoisoning(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold) // 放行泄漏的 fetch 协程，测试收尾干净
	var hung atomic.Bool
	hangOnce := func(ctx context.Context, keys []int) (map[int]int, error) {
		if hung.CompareAndSwap(false, true) {
			<-hold // 首次调用故意无视 ctx 挂死
		}
		out := make(map[int]int, len(keys))
		for _, k := range keys {
			out[k] = k * 10
		}
		return out, nil
	}
	bf := cache.NewBatchFlight[int, int](2, 500, 20*time.Millisecond, hangOnce)
	defer bf.Close()

	start := time.Now()
	_, err := bf.Do(bg(), []int{1})
	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second, "按超时按期返回，不永久阻塞")

	// 无毒化：同一实例后续 Do 照常成功
	out, err := bf.Do(bg(), []int{1})
	require.NoError(t, err)
	require.Equal(t, map[int]int{1: 10}, out)
}

// Close：待取 call 以 ErrClosed resolve（不悬挂），在途批照常完成，之后 Do 拒绝
func TestBatchFlight_Close(t *testing.T) {
	release1 := make(chan struct{})
	g := newGatedFetch(release1)
	bf := cache.NewBatchFlight[int, int](1, 500, time.Second, g.fetch) // K=1

	var wg sync.WaitGroup
	var a map[int]int
	var aErr, bErr error
	wg.Add(2)
	go func() { defer wg.Done(); a, aErr = bf.Do(bg(), []int{1}) }()
	<-g.entered // fetch₁ 在途
	go func() { defer wg.Done(); _, bErr = bf.Do(bg(), []int{2}) }() // 待取
	waitFor(t, func() bool { return bf.Stats().Pending == 1 }, "key2 已登记且待取（K=1，fetch₁ 占槽不被认领）")

	bf.Close()
	close(release1) // 在途批照常完成
	wg.Wait()

	require.ErrorIs(t, bErr, cache.ErrClosed, "待取 call 以 ErrClosed resolve，不悬挂")
	require.NoError(t, aErr)
	require.Equal(t, map[int]int{1: 10}, a)

	_, err := bf.Do(bg(), []int{3})
	require.ErrorIs(t, err, cache.ErrClosed)
	bf.Close() // 幂等
}

// 空 keys / 请求内重复 key / 已取消 ctx 的边界（全默认参数构造）
func TestBatchFlight_EmptyAndDupKeys(t *testing.T) {
	g := newGatedFetch()
	bf := cache.NewBatchFlight[int, int](0, 0, 0, g.fetch) // 0/0/0 → 全默认
	defer bf.Close()

	out, err := bf.Do(bg(), nil)
	require.NoError(t, err)
	require.Empty(t, out)
	require.Equal(t, 0, g.callCount(), "空 keys 不触 fetch")

	out, err = bf.Do(bg(), []int{1, 1, 2, 1})
	require.NoError(t, err)
	require.Equal(t, map[int]int{1: 10, 2: 20}, out)
	require.Equal(t, []int{1, 2}, g.callKeys(0), "请求内去重")

	canceled, cancel := context.WithCancel(bg())
	cancel()
	_, err = bf.Do(canceled, []int{1})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, g.callCount(), "已取消 ctx 不触 fetch")
}

// 并发压力（-race）：随机 key 集，结果归属正确
func TestBatchFlight_StressRace(t *testing.T) {
	slow := func(ctx context.Context, keys []int) (map[int]int, error) {
		time.Sleep(time.Millisecond) // 制造重叠窗口
		out := make(map[int]int, len(keys))
		for _, k := range keys {
			out[k] = k * 10
		}
		return out, nil
	}
	bf := cache.NewBatchFlight[int, int](3, 100, time.Second, slow) // K=3、小批上限促切分
	defer bf.Close()

	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			keys := []int{seed % 7, (seed + 3) % 7, seed % 7} // 刻意含重复
			out, err := bf.Do(bg(), keys)
			assert.NoError(t, err) // goroutine 内用 assert（require.FailNow 仅限测试主协程）
			for _, k := range keys {
				v, ok := out[k]
				assert.True(t, ok, "key %d 应存在", k)
				assert.Equal(t, k*10, v)
			}
		}(g)
	}
	wg.Wait()
}
