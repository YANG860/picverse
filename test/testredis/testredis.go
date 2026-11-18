// Package testredis 提供 cache.Cache 的内存实现（测试替身），与 testdb/testmq 同一模式：
// 不依赖真实 redis，支持 TTL 语义与操作计数，供缓存装饰器断言命中/回填/失效行为。
package testredis

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rednote/packages/cache"
)

type entry struct {
	val       []byte
	expiresAt time.Time // zero 表示不过期
}

// zsetEntry 有序集合条目（ZSET 替身：member → score）
type zsetEntry struct {
	members   map[string]float64
	expiresAt time.Time
}

// Client 内存缓存测试替身（非并发安全计数器场景外整体并发安全）
type Client struct {
	mu sync.Mutex
	m  map[string]entry
	z  map[string]zsetEntry

	// 操作计数（断言用）
	Gets  int
	Hits  int
	Sets  int
	MSets int // MSet 调用次数（RTT 数，区别于 Sets 的 key 数）
	Dels  int
}

var _ cache.Cache = (*Client)(nil)

// New 创建内存缓存测试替身
func New() *Client {
	return &Client{m: make(map[string]entry), z: make(map[string]zsetEntry)}
}

// Get 读取单 key；过期条目按 miss 处理并惰性删除
func (c *Client) Get(ctx context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Gets++
	e, ok := c.m[key]
	if !ok {
		return nil, false, nil
	}
	if c.expired(e) {
		delete(c.m, key)
		return nil, false, nil
	}
	c.Hits++
	// 返回拷贝，防止测试与缓存共享底层数组
	return append([]byte(nil), e.val...), true, nil
}

// MGet 批量读取，结果与 keys 等长对齐；miss 的元素为 nil
func (c *Client) MGet(ctx context.Context, keys []string) ([][]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vals := make([][]byte, len(keys))
	for i, key := range keys {
		c.Gets++
		e, ok := c.m[key]
		if !ok {
			continue
		}
		if c.expired(e) {
			delete(c.m, key)
			continue
		}
		c.Hits++
		vals[i] = append([]byte(nil), e.val...)
	}
	return vals, nil
}

// Set 写入并设置 TTL；ttl <= 0 表示不过期
func (c *Client) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Sets++
	e := entry{val: append([]byte(nil), val...)}
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	c.m[key] = e
	return nil
}

// MSet 批量写入并统一 TTL（计数器按 key 计：Sets += len(pairs)，与逐条 Set 口径一致；
// MSets 计调用次数，供断言"批量回填只占一轮 RTT"）
func (c *Client) MSet(ctx context.Context, pairs []cache.KVPair, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(pairs) == 0 {
		return nil
	}
	c.MSets++
	for _, p := range pairs {
		c.Sets++
		e := entry{val: append([]byte(nil), p.Val...)}
		if ttl > 0 {
			e.expiresAt = time.Now().Add(ttl)
		}
		c.m[p.Key] = e
	}
	return nil
}

// Del 删除 keys（幂等）
func (c *Client) Del(ctx context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Dels++
	for _, key := range keys {
		delete(c.m, key)
	}
	return nil
}

// Len 当前条目数（含未惰性清理的过期条目）
func (c *Client) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// Raw 直接读原始条目（不过期检查前置；断言回填内容用，如空哨兵）
func (c *Client) Raw(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), e.val...), true
}

func (c *Client) expired(e entry) bool {
	return !e.expiresAt.IsZero() && time.Now().After(e.expiresAt)
}

// IncrCounter 原子递增并刷新 TTL（值存十进制字符串；存量脏值按 0 起）
func (c *Client) IncrCounter(ctx context.Context, key string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := int64(0)
	if e, ok := c.m[key]; ok && !c.expired(e) {
		n, _ = strconv.ParseInt(string(e.val), 10, 64)
	}
	n++
	e := entry{val: []byte(strconv.FormatInt(n, 10))}
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	c.m[key] = e
	return nil
}

// TakeCounters 按前缀取走（取走即删；过期条目跳过并清理）
func (c *Client) TakeCounters(ctx context.Context, prefix string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string)
	for key, e := range c.m {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if c.expired(e) {
			delete(c.m, key)
			continue
		}
		out[key] = string(e.val)
		delete(c.m, key)
	}
	return out, nil
}

// ==================== 有序集合原语（ZSET） ====================

// zsetRank序 成员排名序：score 升序、同 score member 字典序升序（redis 原生 rank 口径）
func zsetRankOrder(members map[string]float64) []cache.ZMember {
	out := make([]cache.ZMember, 0, len(members))
	for m, s := range members {
		out = append(out, cache.ZMember{Score: s, Member: m})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].Member < out[j].Member
	})
	return out
}

// ZAdd 追加/更新成员并刷新 TTL（过期 key 视为不存在重建）
func (c *Client) ZAdd(ctx context.Context, key string, members []cache.ZMember, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		ze = zsetEntry{members: make(map[string]float64)}
	}
	for _, m := range members {
		ze.members[m.Member] = m.Score
	}
	ze.expiresAt = time.Time{}
	if ttl > 0 {
		ze.expiresAt = time.Now().Add(ttl)
	}
	c.z[key] = ze
	return nil
}

// ZAddNX 仅不存在的成员写入（不覆盖已有 score）并刷新 TTL（过期 key 视为不存在重建）
func (c *Client) ZAddNX(ctx context.Context, key string, members []cache.ZMember, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		ze = zsetEntry{members: make(map[string]float64)}
	}
	for _, m := range members {
		if _, exists := ze.members[m.Member]; !exists {
			ze.members[m.Member] = m.Score
		}
	}
	ze.expiresAt = time.Time{}
	if ttl > 0 {
		ze.expiresAt = time.Now().Add(ttl)
	}
	c.z[key] = ze
	return nil
}

// ZRevRangeByScore 按 score 降序取 [min, max] 闭区间至多 count 名（同 score member 字典序降序）
func (c *Client) ZRevRangeByScore(ctx context.Context, key string, max, min float64, count int) ([]cache.ZMember, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		if ok {
			delete(c.z, key)
		}
		return nil, nil
	}
	ordered := zsetRankOrder(ze.members)
	var out []cache.ZMember
	for i := len(ordered) - 1; i >= 0; i-- { // 倒序遍历 = score 降序、同 score member 字典序降序
		m := ordered[i]
		if m.Score < min || m.Score > max {
			continue
		}
		out = append(out, m)
		if count > 0 && len(out) >= count {
			break
		}
	}
	return out, nil
}

// ZRemRangeByRank 按排名删除 [start, stop] 闭区间（负数自尾部；越界收敛）
func (c *Client) ZRemRangeByRank(ctx context.Context, key string, start, stop int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		if ok {
			delete(c.z, key)
		}
		return nil
	}
	ordered := zsetRankOrder(ze.members)
	n := len(ordered)
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop >= n {
		stop = n - 1
	}
	// redis 语义：越界收敛后 start > stop 为空操作（如截尾时成员数不足）
	if start > stop || start >= n {
		return nil
	}
	for _, m := range ordered[start : stop+1] {
		delete(ze.members, m.Member)
	}
	return nil
}

// ZRemRangeByScore 按 score 删除 [min, max] 闭区间（±Inf 可用）
func (c *Client) ZRemRangeByScore(ctx context.Context, key string, max, min float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		if ok {
			delete(c.z, key)
		}
		return nil
	}
	for member, sc := range ze.members {
		if sc >= min && sc <= max {
			delete(ze.members, member)
		}
	}
	return nil
}

// ZRevRange 按 score 降序排名取 [start, stop] 闭区间（负数自尾部；同 score member 字典序降序）
func (c *Client) ZRevRange(ctx context.Context, key string, start, stop int) ([]cache.ZMember, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		if ok {
			delete(c.z, key)
		}
		return nil, nil
	}
	ordered := zsetRankOrder(ze.members) // 升序；倒序遍历 = 降序排名 0 起
	n := len(ordered)
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop >= n {
		stop = n - 1
	}
	if start > stop || start >= n {
		return nil, nil
	}
	out := make([]cache.ZMember, 0, stop-start+1)
	for i := n - 1 - start; i >= n-1-stop; i-- { // 降序排名 start..stop
		out = append(out, ordered[i])
	}
	return out, nil
}

// ZRandMember 采样至多 count 名（count <= 0 返回空）。
// 替身取确定性前序（zsetRankOrder 序截取）而非随机——测试可重复（FIRST-R）；
// 真实随机语义由真实 Redis 提供，替身只保证接口形状与集合语义
func (c *Client) ZRandMember(ctx context.Context, key string, count int) ([]cache.ZMember, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		if ok {
			delete(c.z, key)
		}
		return nil, nil
	}
	ordered := zsetRankOrder(ze.members)
	if count > len(ordered) {
		count = len(ordered)
	}
	if count <= 0 {
		return nil, nil
	}
	out := make([]cache.ZMember, 0, count)
	out = append(out, ordered[:count]...)
	return out, nil
}

// ZMScore 批量判定成员存在性（结果等长对齐；过期 key 视为不存在）
func (c *Client) ZMScore(ctx context.Context, key string, members []string) ([]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]bool, len(members))
	ze, ok := c.z[key]
	if !ok || c.zExpired(ze) {
		if ok {
			delete(c.z, key)
		}
		return out, nil
	}
	for i, m := range members {
		_, out[i] = ze.members[m]
	}
	return out, nil
}

// Rename 原子换 key（dst 原值覆盖，TTL 跟随 src；KV 与 ZSET 同命名空间）；src 不存在报错
func (c *Client) Rename(ctx context.Context, src, dst string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	kv, kvOK := c.m[src]
	ze, zOK := c.z[src]
	if (!kvOK || c.expired(kv)) && (!zOK || c.zExpired(ze)) {
		delete(c.m, src)
		delete(c.z, src)
		return fmt.Errorf("testredis rename: no such key %s", src)
	}
	if kvOK && !c.expired(kv) {
		c.m[dst] = kv
	} else {
		delete(c.m, dst)
	}
	if zOK && !c.zExpired(ze) {
		c.z[dst] = ze
	} else {
		delete(c.z, dst)
	}
	delete(c.m, src)
	delete(c.z, src)
	return nil
}

// Exists 判定 key 是否存在（KV 与 ZSET 同命名空间；过期按不存在惰性清理）
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		if c.expired(e) {
			delete(c.m, key)
		} else {
			return true, nil
		}
	}
	if ze, ok := c.z[key]; ok {
		if c.zExpired(ze) {
			delete(c.z, key)
		} else {
			return true, nil
		}
	}
	return false, nil
}

// zExpired 有序集合条目是否过期
func (c *Client) zExpired(e zsetEntry) bool {
	return !e.expiresAt.IsZero() && time.Now().After(e.expiresAt)
}
