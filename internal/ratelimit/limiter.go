package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

type memoryEntry struct {
	count       int
	windowStart time.Time
}

type MemoryLimiter struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{
		entries: make(map[string]memoryEntry),
	}
}

// Allow 使用进程内计数做固定窗口限流。
// 它适合本地开发和单实例部署；多实例部署时应切到 RedisLimiter。
func (l *MemoryLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 || window <= 0 {
		return true, nil
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := l.entries[key]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= window {
		entry = memoryEntry{count: 0, windowStart: now}
	}
	entry.count++
	l.entries[key] = entry
	return entry.count <= limit, nil
}

type RedisLimiter struct {
	client *redis.Client
}

func NewRedisLimiter(client *redis.Client) *RedisLimiter {
	return &RedisLimiter{client: client}
}

// Allow 使用 Redis INCR + EXPIRE 做固定窗口限流。
// 这个实现能跨多个后端实例共享计数，适合后续水平扩展。
func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if l == nil || l.client == nil || limit <= 0 || window <= 0 {
		return true, nil
	}

	count, err := l.client.Incr(ctx, key).Result()
	if err != nil {
		return true, err
	}
	if count == 1 {
		if err := l.client.Expire(ctx, key, window).Err(); err != nil {
			return true, err
		}
	}
	return count <= int64(limit), nil
}
