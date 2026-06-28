package cache

import (
	"context"
	"time"

	"chat_proj/internal/config"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient 根据配置创建 Redis 客户端。
// Redis 是可选依赖：未开启时返回 nil，调用方继续使用内存实现，方便本地开发和测试。
func NewRedisClient(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
