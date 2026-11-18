package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"rednote/packages/defaults"
)

// FetchFunc 批量取数函数：keys 已去重；返回 map 中缺 key 表示"确认不存在"（非错误，
// 调用方按空哨兵口径处理）；返回 error 表示整批失败（错误整批共享）。
// fetch 于构造时绑定（实例内单源、只读）：避免运行期替换的读写竞态与"以谁为准"的语义漂移。
type FetchFunc[K comparable, V any] func(ctx context.Context, keys []K) (map[K]V, error)

// ErrClosed Close 之后 Do 的返回错误
var ErrClosed = errors.New("batchflight: closed")

// 参数默认值以 packages/defaults 为准（config 字段 <= 0 回退 defaults.CacheFlight*），
// 单一默认值来源。


// call 单 key 合并点：多个并发调用者共享一次回源结果。
// done 关闭时 val/ok/err 已就绪（字段写入先于 close，close 建立 happens-before，等待者无竞态读取）。
type call[V any] struct {
	done    chan struct{}
	batched bool // 已被某 worker 认领（在途）；false = 待取（Close 只 fail 待取的）
	val     V
	ok      bool // fetch 结果中存在该 key（DB 确认存在）
	err     error
}

// BatchFlight 批量合并回源（防击穿）：per-key 合并 + 在途批合并，零等待（不做时间窗口）。
//
// 合并与取数解耦：
//   - 合并点是 key（call 表）：任意并发调用重叠的 key 只回源一次——等待者挂到同一 call；
//   - 取数是批（常驻 worker 池）：worker 把待取 key 打成至多 maxBatchSize 的一批一次 fetch，
//     在途并发批数 ≤ maxWorkers（K-worker）。
//
// K 调参的得失：K=1 = 串行波（攒批最优、批最大合并率最高），但有队头阻塞——一个慢批
// 拖住后续所有 key（等待者 ctx 超时即真实失败）；调大 K 消除队头阻塞、吞吐天花板 ×K，
// 代价是凑批变小（合并率下降）、DB 并发放大。SetConcurrency 运行时可调：对后续认领生效，
// 在途批不打断（在途批照常完成，见 safeFetch）。
//
// 语义契约：
//   - 错误整批共享：批查询失败 → 该批所有 key 的等待者拿到同一 error（上抛，非降级）；
//   - 缺 key = 确认不存在（ok=false），非错误——调用方按空哨兵口径处理；
//   - 结果对象跨等待者共享、只读（调用方拷贝组装）；
//   - 发起者取消不掀翻共享查询（fetch 以实例 ctx 派生 + 自带超时）；
//   - 不可毒化：fetch 超时/panic/worker 意外 panic 均保证 call 按期 resolve、名额归还、
//     worker 存活（摘表为所有权凭证，无二次 close）；
//   - 不缓存结果：只合并并发未命中，回填缓存由调用方（cache-aside）负责。
//
// 不做时间窗口合并：认领取"此刻恰好待取"的 key，不为凑包加延迟。
type BatchFlight[K comparable, V any] struct {
	mu       sync.Mutex
	calls    map[K]*call[V] // 未 resolve 的合并点（待取 + 在途都在表里）
	pending  []K            // 待取队列（FIFO；认领时按 maxBatchSize 切批）
	closed   bool
	poolsize int // 当前 worker 池容量（SetConcurrency 只增不减）
	maxBatch int // 单批 key 数上限

	fetch   FetchFunc[K, V] // 构造时绑定，只读
	timeout time.Duration   // 单批 fetch 超时（SetTimeout 可调，mu 保护）
	notify  chan struct{}   // 唤醒信号（cap 1，合并）
	sem     *DynSem         // 动态并发上限
	ctx     context.Context
	cancel  context.CancelFunc

	batches, claimed, fetched, joined int64 // 观测计数（mu 保护）
}

// FlightStats 合并观测计数（命中率分析/测试断言用）。
//   - Batches：批次数；
//   - Claimed：被 worker 认领送去 fetch 的 key 数（批内去重后累计）；
//   - Fetched：fetch 实际返回的 key 数（缺 key = 确认不存在，不计入）；
//   - Joined：被合并掉的请求数（joined 越大说明防击穿合并掉的回源越多）；
//   - Pending：当前待取 key 数（持续增长 = 回源供不上，吞吐天花板信号，考虑调大 K）。
type FlightStats struct {
	Batches int64
	Claimed int64
	Fetched int64
	Joined  int64
	Pending int
}

// NewBatchFlight 创建批量合并回源器：
//   - maxWorkers：并发批上限 K，兼 worker 池初始容量（<= 0 回退 defaults.CacheFlightWorkers）；
//   - maxBatchSize：单批 key 数上限，积压自动切分多批（<= 0 回退 defaults.CacheFlightBatchSize）；
//   - fetchTimeout：单批 fetch 超时（<= 0 回退 defaults.CacheFlightTimeout），防挂死毒化底线；
//   - fetch：取数函数，构造时绑定（实例内单源）。
func NewBatchFlight[K comparable, V any](
	maxWorkers, maxBatchSize int,
	fetchTimeout time.Duration,
	fetch FetchFunc[K, V],
) *BatchFlight[K, V] {
	if maxWorkers <= 0 {
		maxWorkers = defaults.CacheFlightWorkers
	}
	if maxBatchSize <= 0 {
		maxBatchSize = defaults.CacheFlightBatchSize
	}
	if fetchTimeout <= 0 {
		fetchTimeout = defaults.CacheFlightTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &BatchFlight[K, V]{
		calls:    make(map[K]*call[V]),
		poolsize: maxWorkers,
		maxBatch: maxBatchSize,
		fetch:    fetch,
		timeout:  fetchTimeout,
		notify:   make(chan struct{}, 1),
		sem:      NewDynSem(maxWorkers),
		ctx:      ctx,
		cancel:   cancel,
	}
	for i := 0; i < maxWorkers; i++ {
		go b.runWorker()
	}
	return b
}

// Do 合并回源：返回 map 含 fetch 确认存在的 key（值共享只读）；
// 确认不存在的 key 不在 map 中（无 error）；批失败返回 error；Close 后返回 ErrClosed。
// 空 keys（或全重复）不触 fetch，直接返回空 map。
//
// 语义：全成或错。跨批部分失败时（不同 key 被不同批认领，其中一批失败），整体返回 error，
// 已成功 key 的结果被丢弃——cache-aside 场景通常可接受（失败重试）。
// 若需"尽力而为"（部分成功也返回），用 DoBestEffort。
//
// ctx 仅控制本调用等待；不传递给 fetch（共享查询不受单个发起者取消影响）。
func (b *BatchFlight[K, V]) Do(ctx context.Context, keys []K) (map[K]V, error) {
	out, err := b.DoBestEffort(ctx, keys)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DoBestEffort 与 Do 相同，但跨批部分失败时返回已成功 key 的结果 + 首个 error。
// 注意：返回的 map 可能非空且 err 非 nil。调用方应视为"部分成功"。
//
// ctx 仅控制本调用等待；不传递给 fetch。
func (b *BatchFlight[K, V]) DoBestEffort(ctx context.Context, keys []K) (map[K]V, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uniq := dedupeKeys(keys) // 请求内去重（保首次出现序）
	if len(uniq) == 0 {
		return map[K]V{}, nil
	}

	// ── 阶段 1：登记合并点（单次加锁）──
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	my := make([]*call[V], len(uniq))
	newKeys := 0
	for i, k := range uniq {
		c, ok := b.calls[k]
		if !ok {
			c = &call[V]{done: make(chan struct{})}
			b.calls[k] = c
			b.pending = append(b.pending, k)
			newKeys++
		} else {
			b.joined++
		}
		my[i] = c
	}
	b.mu.Unlock()

	// 有新 key → 唤醒一个 worker（notify cap 1 自然合并）
	if newKeys > 0 {
		b.wake()
	}

	// ── 阶段 2：等待自己的 call ──
	out := make(map[K]V, len(uniq))
	var firstErr error
	for i, k := range uniq {
		c := my[i]
		// 先无阻塞探 done 再 select：done 与 ctx 同时就绪时 select 随机挑，
		// 否则结果已就绪却可能误报 ctx 错误
		select {
		case <-c.done:
		default:
			select {
			case <-c.done:
			case <-ctx.Done():
				return out, ctx.Err()
			}
		}
		if c.err != nil {
			if firstErr == nil {
				firstErr = c.err
			}
			continue // 尽力而为：继续收集其它 key
		}
		if c.ok {
			out[k] = c.val
		}
	}
	return out, firstErr
}

// Close 关闭：拒绝新 Do（ErrClosed）；待取 call 以 ErrClosed resolve（等待者不悬挂），
// 在途批照常完成（fetch 自带超时兜底）后 worker 退出。幂等。
func (b *BatchFlight[K, V]) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	failed := make([]*call[V], 0)
	for k, c := range b.calls {
		if !c.batched { // 只 fail 待取的；在途批的 call 由其 worker 正常 resolve
			delete(b.calls, k)
			c.err = ErrClosed
			failed = append(failed, c)
		}
	}
	b.pending = nil
	b.cancel()
	b.mu.Unlock()
	for _, c := range failed {
		close(c.done)
	}
}

// SetConcurrency 运行时调整并发批上限（1 = 退回串行）。
// 对后续认领生效：在途批不打断（照常完成），仅新批受新上限约束。
// 先抬信号量上限再补 worker（顺序保证新 worker 立即可用）；worker 池只增不减
// （闲置 worker 在 select 上挂起，成本可忽略）。
func (b *BatchFlight[K, V]) SetConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	b.sem.SetLimit(n)
	b.mu.Lock()
	for b.poolsize < n {
		b.poolsize++
		go b.runWorker()
	}
	b.mu.Unlock()
}

// SetTimeout 运行时调整单批 fetch 超时（对后续批生效，在途批沿用启动时的超时）。
// n <= 0 忽略（保持原值）。
func (b *BatchFlight[K, V]) SetTimeout(n time.Duration) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.timeout = n
	b.mu.Unlock()
}

// wake 发一次唤醒信号（notify cap 1：已有未消费信号时自然合并）
func (b *BatchFlight[K, V]) wake() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// runWorker 常驻 worker：被 notify 唤醒即认领执行，Close 后退出。
// 失活保护在 claimAndRun 内（per-batch/per-worker 两层 recover），worker 循环不中断。
func (b *BatchFlight[K, V]) runWorker() {
	for {
		select {
		case <-b.notify:
		case <-b.ctx.Done():
			return
		}
		b.claimAndRun()
	}
}

// claimAndRun 抢并发槽 → 认领一批（至多 maxBatchSize）→ 执行。
// 兜底链：safeFetch 把 fetch panic 转整批错误 → 此处 recover 兜框架级意外
// （已认领的批以错误 resolve、名额由外层 defer 归还、worker 循环继续存活）。
func (b *BatchFlight[K, V]) claimAndRun() {
	b.sem.Acquire()
	defer b.sem.Release()

	var batch []K
	var cs []*call[V]
	defer func() {
		if r := recover(); r != nil {
			// 框架级意外 panic：摘表所有权凭证保证不二次 close，call 不悬挂
			b.resolveCalls(batch, cs, nil, fmt.Errorf("batchflight: worker panic: %v", r))
		}
	}()

	// 认领待取 key（FIFO 切至多 maxBatchSize 一批）
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	n := min(len(b.pending), b.maxBatch)
	batch = append([]K(nil), b.pending[:n]...) // 拷贝：与后续 append 的 pending 解耦
	// 头部前移；队列清空时重置为 nil，避免底层数组滞留已消费 key（长跑内存滞留）
	if n == len(b.pending) {
		b.pending = nil
	} else {
		b.pending = b.pending[n:]
	}
	cs = make([]*call[V], len(batch))
	for i, k := range batch {
		c, ok := b.calls[k] // 不变式：pending 中的 key 必有未认领 call
		if !ok {
			// 防御：理论上不可达（Close 同步清 pending 与 calls）；置 nil 让 resolveCalls 跳过
			continue
		}
		cs[i] = c
		c.batched = true
	}
	more := len(b.pending) > 0
	b.batches++
	b.claimed += int64(len(batch))
	b.mu.Unlock()

	if more {
		b.wake() // 批上限切分出余量 → 补一次唤醒（不丢工作、不串行排队）
	}

	// 快照超时（在途批沿用启动时值，SetTimeout 不影响本批）
	b.mu.Lock()
	timeout := b.timeout
	b.mu.Unlock()

	vals, err := b.safeFetch(b.ctx, batch, timeout)
	b.resolveCalls(batch, cs, vals, err)
}

// resolveCalls 以"从表中摘除"为所有权凭证 resolve 一批 call：摘到者写结果并 close(done)，
// 摘不到（已被其它路径兜底）即跳过——杜绝二次 close / 结果写在 close 之后的竞态。
// err != nil 时错误整批共享；vals 缺 key 即确认不存在（ok=false）。
func (b *BatchFlight[K, V]) resolveCalls(batch []K, cs []*call[V], vals map[K]V, err error) {
	b.mu.Lock()
	owned := make([]int, 0, len(batch))
	for i, k := range batch {
		if cs[i] == nil {
			continue
		}
		if cur, ok := b.calls[k]; ok && cur == cs[i] {
			delete(b.calls, k)
			owned = append(owned, i)
		}
	}
	if err == nil {
		b.fetched += int64(len(vals))
	}
	b.mu.Unlock()

	for _, i := range owned {
		if err != nil {
			cs[i].err = err
		} else {
			cs[i].val, cs[i].ok = vals[batch[i]]
		}
		close(cs[i].done)
	}
}

// safeFetch 带防护的单批取数：
//   - 与取消隔离：fetch 从 base 剥离取消（WithoutCancel）再套超时——Close/worker 退出
//     不杀在途批（“在途批照常完成”语义），fetch 只受自身超时约束；
//   - 超时兜底：即使 fetch 不理会 ctx 也保证批次按期 resolve（被放弃的 fetch 协程
//     最坏泄漏一个，换取 BatchFlight 整体不被毒化）；
//   - panic 转错误：fetch panic → 整批共享 error，绝不让 panic 杀协程。
//
// 超时错误用 %w 包 context.DeadlineExceeded，调用方可用 errors.Is(err, context.DeadlineExceeded) 判断。
func (b *BatchFlight[K, V]) safeFetch(base context.Context, keys []K, timeout time.Duration) (map[K]V, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(base), timeout)
	defer cancel()

	type result struct {
		vals map[K]V
		err  error
	}
	ch := make(chan result, 1) // 缓冲 1：被放弃的 fetch 返回时不阻塞
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("batchflight: fetch panic: %v", r)}
			}
		}()
		vals, err := b.fetch(ctx, keys)
		ch <- result{vals: vals, err: err}
	}()

	select {
	case r := <-ch:
		return r.vals, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("batchflight: fetch %d keys: %w", len(keys), ctx.Err())
	}
}

// Stats 返回合并观测计数快照
func (b *BatchFlight[K, V]) Stats() FlightStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return FlightStats{
		Batches: b.batches,
		Claimed: b.claimed,
		Fetched: b.fetched,
		Joined:  b.joined,
		Pending: len(b.pending),
	}
}

// DynSem 动态并发上限信号量：Acquire 阻塞至有名额；SetLimit 运行时调上限
// （调小自然收缩，调大唤醒增量名额）。
type DynSem struct {
	mu    sync.Mutex
	cond  *sync.Cond
	cur   int // 已占用名额
	limit int // 上限
}

// NewDynSem 创建动态信号量（limit < 1 置 1）
func NewDynSem(limit int) *DynSem {
	if limit < 1 {
		limit = 1
	}
	s := &DynSem{limit: limit}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Acquire 占一个名额（阻塞至 cur < limit）
func (s *DynSem) Acquire() {
	s.mu.Lock()
	for s.cur >= s.limit {
		s.cond.Wait()
	}
	s.cur++
	s.mu.Unlock()
}

// Release 归还名额并唤醒一个等待者
func (s *DynSem) Release() {
	s.mu.Lock()
	s.cur--
	s.cond.Signal()
	s.mu.Unlock()
}

// SetLimit 调整上限（n < 1 置 1）：调大时按增量唤醒等待者。
// 调小只影响后续 Acquire（已在 Acquire 之后的调用不受影响）。
func (s *DynSem) SetLimit(n int) {
	if n < 1 {
		n = 1
	}
	s.mu.Lock()
	delta := n - s.limit
	s.limit = n
	s.mu.Unlock()
	if delta > 0 {
		for range delta {
			s.cond.Signal()
		}
	}
}

// dedupeKeys 按首次出现顺序去重（请求内重复 key 不重复登记/回源）
func dedupeKeys[K comparable](keys []K) []K {
	uniq := make([]K, 0, len(keys))
	seen := make(map[K]struct{}, len(keys))
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, k)
	}
	return uniq
}
