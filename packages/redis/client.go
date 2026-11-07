package redis

import (
	"context"
	"fmt"
	"log/slog"

	"rednote/config"
	"rednote/packages/logger"

	goredis "github.com/redis/go-redis/v9"
)

// RedisClient go-redis 客户端封装（连接工厂）
// 内嵌 *goredis.Client，底层命令可直接调用（如 r.Get / r.SetNX）；
// 业务语义（缓存存取、计数器聚合）见 packages/cache，本包只负责建连
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
	rc.log.Info("redis connected", "addr", cfg.Redis.Addr, "db", cfg.Redis.DB)

	return rc, func() {
		_ = rc.Close()
	}
}
