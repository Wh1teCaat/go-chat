package ws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// PresenceStore 是 websocket 层依赖的最小在线状态接口。
// 具体实现放在 internal/presence，避免 ws 包直接依赖 Redis。
type PresenceStore interface {
	Connect(ctx context.Context, userID uint, connectionID string) error
	Disconnect(ctx context.Context, userID uint, connectionID string) error
	Refresh(ctx context.Context, userID uint, connectionID string) error
}

func newConnectionID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}
