package service

import "context"

type onlineStatusStore interface {
	ListOnline(ctx context.Context, userIDs []uint) (map[uint]bool, error)
}

type noopOnlineStatusStore struct{}

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

func listOnlineStatuses(ctx context.Context, userIDs []uint) map[uint]bool {
	online, err := presenceStatusStore.ListOnline(ctx, userIDs)
	if err != nil {
		return map[uint]bool{}
	}
	return online
}
