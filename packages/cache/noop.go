package cache

import (
	"context"
	"time"
)

// Noop 空实现：始终 miss、写删空转。
// 用于 cache.enabled=false 时注入（服务可无 redis 启动，行为等同无缓存），
// 调用方零分支判断。
type Noop struct{}

var _ Cache = (*Noop)(nil)

// NewNoop 创建空实现
func NewNoop() *Noop {
	return &Noop{}
}

// Get 始终 miss
func (Noop) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return nil, false, nil
}

// MGet 全部 miss
func (Noop) MGet(ctx context.Context, keys []string) ([][]byte, error) {
	return make([][]byte, len(keys)), nil
}

// Set 空转
func (Noop) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return nil
}

// Del 空转
func (Noop) Del(ctx context.Context, keys ...string) error {
	return nil
}

// IncrCounter 空转（无缓存时计数不累积，浏览计数等聚合降级为不计）
func (Noop) IncrCounter(ctx context.Context, key string, ttl time.Duration) error {
	return nil
}

// ZAdd 空转
func (Noop) ZAdd(ctx context.Context, key string, members []ZMember, ttl time.Duration) error {
	return nil
}

// ZRevRangeByScore 恒空（无缓存 → 调用方按 miss 回源）
func (Noop) ZRevRangeByScore(ctx context.Context, key string, max, min float64, count int) ([]ZMember, error) {
	return nil, nil
}

// ZRemRangeByRank 空转
func (Noop) ZRemRangeByRank(ctx context.Context, key string, start, stop int) error {
	return nil
}

// ZRemRangeByScore 空转
func (Noop) ZRemRangeByScore(ctx context.Context, key string, max, min float64) error {
	return nil
}

// Exists 恒 false（无缓存 → 调用方按 miss 回源）
func (Noop) Exists(ctx context.Context, key string) (bool, error) {
	return false, nil
}

// TakeCounters 恒空（无可取增量）
func (Noop) TakeCounters(ctx context.Context, prefix string) (map[string]string, error) {
	return map[string]string{}, nil
}
