// Package testutil 提供测试专用 Redis 协议服务，不作为生产缓存实现。
package testutil

import (
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"testing"
)

// Redis 启动隔离的 Redis 协议测试服务。
func Redis(t *testing.T) *redis.Client {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}
