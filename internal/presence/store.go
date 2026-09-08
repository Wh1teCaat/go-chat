package presence

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultPresenceTTL = 2 * time.Minute

// Store 记录 websocket 连接维度的在线状态。
// 一个用户可能有多个连接；只要还有任意连接存活，用户就被认为在线。
type Store interface {
	Connect(ctx context.Context, userID uint, connectionID string) error
	Disconnect(ctx context.Context, userID uint, connectionID string) error
	Refresh(ctx context.Context, userID uint, connectionID string) error
	ListOnline(ctx context.Context, userIDs []uint) (map[uint]bool, error)
}

type MemoryStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	conns map[uint]map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	return newMemoryStore(defaultPresenceTTL)
}

func newMemoryStore(ttl time.Duration) *MemoryStore {
	if ttl <= 0 {
		ttl = defaultPresenceTTL
	}
	return &MemoryStore{
		ttl:   ttl,
		conns: make(map[uint]map[string]time.Time),
	}
}

// Connect 在内存中登记用户的一条在线连接并设置过期时间。
func (s *MemoryStore) Connect(ctx context.Context, userID uint, connectionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if userID == 0 || connectionID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conns[userID] == nil {
		s.conns[userID] = make(map[string]time.Time)
	}
	s.conns[userID][connectionID] = time.Now().Add(s.ttl)
	return nil
}

// Disconnect 从内存中移除用户的一条在线连接。
func (s *MemoryStore) Disconnect(ctx context.Context, userID uint, connectionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if userID == 0 || connectionID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.conns[userID], connectionID)
	if len(s.conns[userID]) == 0 {
		delete(s.conns, userID)
	}
	return nil
}

// Refresh 延长内存中指定在线连接的有效期。
func (s *MemoryStore) Refresh(ctx context.Context, userID uint, connectionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if userID == 0 || connectionID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conns[userID] == nil {
		return nil
	}
	if _, ok := s.conns[userID][connectionID]; ok {
		s.conns[userID][connectionID] = time.Now().Add(s.ttl)
	}
	return nil
}

// ListOnline 批量查询用户在内存状态存储中的在线情况。
func (s *MemoryStore) ListOnline(ctx context.Context, userIDs []uint) (map[uint]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	result := make(map[uint]bool, len(userIDs))
	for _, userID := range userIDs {
		for connID, expiresAt := range s.conns[userID] {
			if now.After(expiresAt) {
				delete(s.conns[userID], connID)
			}
		}
		if len(s.conns[userID]) == 0 {
			delete(s.conns, userID)
			continue
		}
		result[userID] = true
	}
	return result, nil
}

type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{
		client: client,
		ttl:    defaultPresenceTTL,
	}
}

// Connect 在 Redis 中登记用户连接，并同时设置连接和用户集合的有效期。
func (s *RedisStore) Connect(ctx context.Context, userID uint, connectionID string) error {
	if s == nil || s.client == nil || userID == 0 || connectionID == "" {
		return nil
	}

	pipe := s.client.Pipeline()
	pipe.SAdd(ctx, userSetKey(userID), connectionID)
	pipe.Expire(ctx, userSetKey(userID), s.ttl)
	pipe.Set(ctx, connectionKey(connectionID), strconv.FormatUint(uint64(userID), 10), s.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// Disconnect 从 Redis 中移除连接，并清理不再包含有效连接的用户集合。
func (s *RedisStore) Disconnect(ctx context.Context, userID uint, connectionID string) error {
	if s == nil || s.client == nil || userID == 0 || connectionID == "" {
		return nil
	}

	setKey := userSetKey(userID)
	pipe := s.client.Pipeline()
	pipe.SRem(ctx, setKey, connectionID)
	pipe.Del(ctx, connectionKey(connectionID))
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}

	count, err := s.client.SCard(ctx, setKey).Result()
	if err != nil {
		return err
	}
	if count == 0 {
		return s.client.Del(ctx, setKey).Err()
	}
	return s.client.Expire(ctx, setKey, s.ttl).Err()
}

// Refresh 延长 Redis 中连接标记和用户连接集合的有效期。
func (s *RedisStore) Refresh(ctx context.Context, userID uint, connectionID string) error {
	if s == nil || s.client == nil || userID == 0 || connectionID == "" {
		return nil
	}

	pipe := s.client.Pipeline()
	pipe.Expire(ctx, userSetKey(userID), s.ttl)
	pipe.Expire(ctx, connectionKey(connectionID), s.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// ListOnline 批量查询用户在 Redis 状态存储中的在线情况。
func (s *RedisStore) ListOnline(ctx context.Context, userIDs []uint) (map[uint]bool, error) {
	result := make(map[uint]bool, len(userIDs))
	if s == nil || s.client == nil {
		return result, nil
	}

	for _, userID := range userIDs {
		online, err := s.userOnline(ctx, userID)
		if err != nil {
			return nil, err
		}
		if online {
			result[userID] = true
		}
	}
	return result, nil
}

// userOnline 清理失效连接后判断指定用户是否仍有活跃连接。
func (s *RedisStore) userOnline(ctx context.Context, userID uint) (bool, error) {
	if userID == 0 {
		return false, nil
	}

	setKey := userSetKey(userID)
	connectionIDs, err := s.client.SMembers(ctx, setKey).Result()
	if err != nil {
		return false, err
	}
	if len(connectionIDs) == 0 {
		return false, nil
	}

	active := 0
	stale := make([]interface{}, 0)
	for _, connectionID := range connectionIDs {
		exists, err := s.client.Exists(ctx, connectionKey(connectionID)).Result()
		if err != nil {
			return false, err
		}
		if exists > 0 {
			active++
			continue
		}
		stale = append(stale, connectionID)
	}

	if len(stale) > 0 {
		if err := s.client.SRem(ctx, setKey, stale...).Err(); err != nil {
			return false, err
		}
	}
	if active == 0 {
		if err := s.client.Del(ctx, setKey).Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, s.client.Expire(ctx, setKey, s.ttl).Err()
}

// userSetKey 返回保存用户连接集合的 Redis 键。
func userSetKey(userID uint) string {
	return fmt.Sprintf("presence:user:%d:connections", userID)
}

// connectionKey 返回保存单条连接心跳状态的 Redis 键。
func connectionKey(connectionID string) string {
	return "presence:connection:" + connectionID
}
