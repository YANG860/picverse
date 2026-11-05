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
