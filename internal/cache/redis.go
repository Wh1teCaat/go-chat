package cache

import (
	"context"
	"errors"
	"time"

	"chat_proj/internal/config"

	"github.com/redis/go-redis/v9"
)

// RedisStore 使用 Redis 保存缓存和刷新令牌状态。
type RedisStore struct{ client *redis.Client }

func NewRedisStore(client *redis.Client) *RedisStore { return &RedisStore{client: client} }

// ready 检查存储依赖是否已注入。
func (s *RedisStore) ready() error {
	if s == nil || s.client == nil {
		return errors.New("redis store is not initialized")
	}
	return nil
}

// GetString 读取字符串，区分空值和未命中。
func (s *RedisStore) GetString(ctx context.Context, key string) (string, bool, error) {
	if err := s.ready(); err != nil {
		return "", false, err
	}
	v, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetString 保存字符串并设置有效期。
func (s *RedisStore) SetString(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := s.ready(); err != nil {
		return err
	}
	if ttl.Milliseconds() <= 0 {
		return errors.New("TTL must be at least one millisecond")
	}
	return s.client.Set(ctx, key, value, ttl).Err()
}

// GetHash 读取 Hash 的完整字段集合。
func (s *RedisStore) GetHash(ctx context.Context, key string) (map[string]string, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	fields, err := s.client.HGetAll(ctx, key).Result()
	return fields, len(fields) > 0 && err == nil, err
}

// SetHash 使用原生 HSET 更新指定字段，并在事务中刷新整个键的 TTL；保留其他字段。
func (s *RedisStore) SetHash(ctx context.Context, key string, fields map[string]string, ttl time.Duration) error {
	if err := s.ready(); err != nil {
		return err
	}
	if ttl.Milliseconds() <= 0 || len(fields) == 0 {
		return errors.New("positive TTL and nonempty hash required")
	}
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, fields)
		pipe.PExpire(ctx, key, ttl)
		return nil
	})
	return err
}

// Delete 删除指定键。
func (s *RedisStore) Delete(ctx context.Context, keys ...string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	return s.client.Del(ctx, keys...).Err()
}

var rotateStringScript = redis.NewScript(`
if redis.call("GET",KEYS[1]) ~= ARGV[1] then 
	return 0 
end

if not redis.call("SET",KEYS[2],ARGV[2],"PX",ARGV[3],"NX") then 
	return -1 
end

redis.call("DEL",KEYS[1])
return 1
`)

// RotateString 校验旧值后原子替换键，新键冲突时保留旧键。
func (s *RedisStore) RotateString(ctx context.Context, oldKey, newKey, expected, value string, ttl time.Duration) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	if oldKey == newKey || ttl.Milliseconds() <= 0 {
		return false, errors.New("distinct keys and positive TTL required")
	}
	n, err := rotateStringScript.Run(ctx, s.client, []string{oldKey, newKey}, expected, value, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	if n == -1 {
		return false, errors.New("new token key already exists")
	}
	return n == 1, nil
}

// NewRedisClient 根据配置创建 Redis 客户端。
// Redis 是必需依赖，禁用或连接失败时拒绝启动。
func NewRedisClient(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	if !cfg.Enabled {
		return nil, errors.New("redis must be enabled")
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
