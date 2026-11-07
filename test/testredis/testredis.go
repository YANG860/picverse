// Package testredis 提供 cache.Cache 的内存实现（测试替身），与 testdb/testmq 同一模式：
// 不依赖真实 redis，支持 TTL 语义与操作计数，供缓存装饰器断言命中/回填/失效行为。
package testredis

import (
	"context"
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

// Client 内存缓存测试替身（非并发安全计数器场景外整体并发安全）
type Client struct {
	mu sync.Mutex
	m  map[string]entry

	// 操作计数（断言用）
	Gets int
	Hits int
	Sets int
	Dels int
}

var _ cache.Cache = (*Client)(nil)

// New 创建内存缓存测试替身
func New() *Client {
	return &Client{m: make(map[string]entry)}
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
