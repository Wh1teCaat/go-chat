package cache

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Store interface {
	GetJSON(ctx context.Context, key string, dest any) (bool, error)
	SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

type noopStore struct{}

func NewNoopStore() Store {
	return noopStore{}
}

func (noopStore) GetJSON(context.Context, string, any) (bool, error) {
	return false, nil
}

func (noopStore) SetJSON(context.Context, string, any, time.Duration) error {
	return nil
}

func (noopStore) Delete(context.Context, ...string) error {
	return nil
}

type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(client *redis.Client) Store {
	if client == nil {
		return NewNoopStore()
	}
	return &RedisStore{client: client}
}

// GetJSON 从 Redis 读取 JSON 缓存。redis.Nil 表示未命中，不算业务错误。
func (s *RedisStore) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	raw, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return false, err
	}
	return true, nil
}

func (s *RedisStore) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, key, raw, ttl).Err()
}

func (s *RedisStore) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.client.Del(ctx, keys...).Err()
}

type memoryItem struct {
	data      []byte
	expiresAt time.Time
}

type MemoryStore struct {
	mu    sync.Mutex
	items map[string]memoryItem
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]memoryItem)}
}

// MemoryStore 主要用于测试 cache-aside 行为；生产数据库缓存默认使用 RedisStore。
func (s *MemoryStore) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	s.mu.Lock()
	item, ok := s.items[key]
	if ok && !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
		delete(s.items, key)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(item.data, dest)
}

func (s *MemoryStore) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}

	s.mu.Lock()
	s.items[key] = memoryItem{data: raw, expiresAt: expiresAt}
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, keys ...string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	for _, key := range keys {
		delete(s.items, key)
	}
	s.mu.Unlock()
	return nil
}
