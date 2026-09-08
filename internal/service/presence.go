package service

import "context"

type onlineStatusStore interface {
	ListOnline(ctx context.Context, userIDs []uint) (map[uint]bool, error)
}

type noopOnlineStatusStore struct{}

// ListOnline 在空状态存储中为所有查询返回离线结果。
func (noopOnlineStatusStore) ListOnline(context.Context, []uint) (map[uint]bool, error) {
	return map[uint]bool{}, nil
}

var presenceStatusStore onlineStatusStore = noopOnlineStatusStore{}

func InitPresenceStore(store onlineStatusStore) {
	if store == nil {
		store = noopOnlineStatusStore{}
	}
	presenceStatusStore = store
}

// listOnlineStatuses 查询用户在线状态，并在存储失败时降级为全部离线。
func listOnlineStatuses(ctx context.Context, userIDs []uint) map[uint]bool {
	online, err := presenceStatusStore.ListOnline(ctx, userIDs)
	if err != nil {
		return map[uint]bool{}
	}
	return online
}
