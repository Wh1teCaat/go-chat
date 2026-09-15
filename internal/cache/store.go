package cache

import (
	"context"
	"time"
)

// Store 提供 String 与 Hash 缓存，bool 表示命中。
type Store interface {
	GetString(context.Context, string) (string, bool, error)
	SetString(context.Context, string, string, time.Duration) error
	GetHash(context.Context, string) (map[string]string, bool, error)
	// SetHash 更新指定字段并刷新 TTL，保留未传入的字段。
	SetHash(context.Context, string, map[string]string, time.Duration) error
	Delete(context.Context, ...string) error
}

// TokenStore 额外提供原子轮换。
type TokenStore interface {
	Store
	RotateString(context.Context, string, string, string, string, time.Duration) (bool, error)
}
