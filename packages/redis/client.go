package redis

import (
	"context"
	"fmt"
	"log/slog"

	"rednote/config"
	"rednote/packages/logger"

	goredis "github.com/redis/go-redis/v9"
)

// RedisRepo Redis 工具仓库
// TODO: 具体方法稍后补全
type RedisRepo interface {
	// HealthCheck 健康检查
	HealthCheck(ctx context.Context) error
}

// RedisClient go-redis 客户端封装
// 内嵌 *goredis.Client，底层命令可直接调用（如 r.Get / r.SetNX）
type RedisClient struct {
	*goredis.Client
	log *slog.Logger
}

// NewClient 创建 go-redis 客户端
// cfg 从 config.RedisConfig 获取配置
func NewClient(cfg *config.Config, lf logger.LoggerFactory) (*RedisClient, func()) {
	if cfg == nil {
		panic(fmt.Errorf("redis config is required"))
	}

	client := goredis.NewClient(&goredis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
	})

	// 启动时校验连通性，失败 fail-fast
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		panic(fmt.Errorf("failed to ping redis: %w", err))
	}

	rc := &RedisClient{
		Client: client,
		log:    lf.GetLoggerOf("redis"),
	}

	return rc, func() {
		_ = rc.Close()
	}
}

// HealthCheck 健康检查
func (r *RedisClient) HealthCheck(ctx context.Context) error {
	if err := r.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis health check failed: %w", err)
	}
	return nil
}
