package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rednote/config"
	"rednote/packages/logger"
	"rednote/packages/redis"

	goredis "github.com/redis/go-redis/v9"
)

// RedisCache 基于 go-redis 的 Cache 实现（生产 L2 共享缓存）。
// 只做原语映射，错误上抛由调用方降级（fail-open），不在本层吞错。
type RedisCache struct {
	rc *redis.RedisClient
}

var _ Cache = (*RedisCache)(nil)

// NewRedisCache 创建 RedisCache
func NewRedisCache(rc *redis.RedisClient) *RedisCache {
	return &RedisCache{rc: rc}
}

// NewCache 创建缓存客户端：
//   - cfg.Cache.Enabled=false：返回 Noop，不建立 redis 连接（服务可无 redis 启动，行为等同无缓存）
//   - cfg.Cache.Enabled=true：创建 go-redis 客户端，启动 ping fail-fast（沿用 packages/redis 风格，
//     配置启用但连不上视为部署错误）；运行期故障由调用方降级回源数据库
func NewCache(cfg *config.Config, lf logger.LoggerFactory) (Cache, func()) {
	if cfg == nil {
		panic(fmt.Errorf("cache: config is required"))
	}
	if !cfg.Cache.Enabled {
		return NewNoop(), func() {}
	}
	rc, cleanup := redis.NewClient(cfg, lf)
	return NewRedisCache(rc), cleanup
}

// Get 读取单 key；key 不存在返回 ok=false，其余错误上抛
func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := r.rc.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cache get %s: %w", key, err)
	}
	return val, true, nil
}

// MGet 批量读取，结果与 keys 等长对齐；miss 的元素为 nil
func (r *RedisCache) MGet(ctx context.Context, keys []string) ([][]byte, error) {
	vals := make([][]byte, len(keys))
	if len(keys) == 0 {
		return vals, nil
	}
	res, err := r.rc.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("cache mget %v: %w", keys, err)
	}
	for i, v := range res {
		if v == nil {
			continue // key 不存在
		}
		s, ok := v.(string)
		if !ok {
			// go-redis MGET 只会返回 string 或 nil，走到这里说明响应异常
			return nil, fmt.Errorf("cache mget %s: unexpected value type %T", keys[i], v)
		}
		vals[i] = []byte(s)
	}
	return vals, nil
}

// Set 写入并设置 TTL；ttl <= 0 表示不过期
func (r *RedisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := r.rc.Set(ctx, key, val, ttl).Err(); err != nil {
		return fmt.Errorf("cache set %s: %w", key, err)
	}
	return nil
}

// Del 删除 keys；不存在的 key 静默跳过（redis DEL 语义天然幂等）
func (r *RedisCache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := r.rc.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cache del %v: %w", keys, err)
	}
	return nil
}

// IncrCounter 原子递增并刷新 TTL（INCR + EXPIRE 管道提交）
func (r *RedisCache) IncrCounter(ctx context.Context, key string, ttl time.Duration) error {
	pipe := r.rc.Pipeline()
	pipe.Incr(ctx, key)
	if ttl > 0 {
		pipe.Expire(ctx, key, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("cache incr counter %s: %w", key, err)
	}
	return nil
}

// TakeCounters SCAN 逐 key GETDEL 原子取走（多消费者互斥：取走者独得增量）。
// 取走即清除，Redis 是增量的唯一事实源；脏值原样上交，由调用方裁决。
func (r *RedisCache) TakeCounters(ctx context.Context, prefix string) (map[string]string, error) {
	out := make(map[string]string)
	var cursor uint64
	for {
		keys, next, err := r.rc.Scan(ctx, cursor, prefix+"*", 200).Result()
		if err != nil {
			return nil, fmt.Errorf("cache take counters scan %s: %w", prefix, err)
		}
		for _, key := range keys {
			val, err := r.rc.GetDel(ctx, key).Result()
			if errors.Is(err, goredis.Nil) {
				continue // 竞态：已被其他取走者清掉
			}
			if err != nil {
				return nil, fmt.Errorf("cache take counters getdel %s: %w", key, err)
			}
			out[key] = val
		}
		cursor = next
		if cursor == 0 {
			return out, nil
		}
	}
}
