package service

import (
	"context"
	"time"

	"chat_proj/internal/cache"
	"chat_proj/internal/model"
)

const groupInfoCacheTTL = 10 * time.Minute

type cachedGroupInfo struct {
	ID        uint      `json:"id"`
	Name      string    `json:"name"`
	OwnerID   uint      `json:"ownerID"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func getGroupInfo(ctx context.Context, groupID uint) (*model.Group, error) {
	if groupID == 0 {
		return repo.GetGroupByID(ctx, groupID)
	}

	key := cache.GroupInfoKey(groupID)
	var cached cachedGroupInfo
	ok, err := cacheStore.GetJSON(ctx, key, &cached)
	if err != nil {
		logCacheError("GetGroupInfoCacheFailed", key, err)
	} else if ok {
		return cached.toModel(), nil
	}

	group, err := repo.GetGroupByID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	setGroupInfoCache(ctx, *group)
	return group, nil
}

func setGroupInfoCache(ctx context.Context, group model.Group) {
	if group.ID == 0 {
		return
	}
	info := cachedGroupInfo{
		ID:        group.ID,
		Name:      group.Name,
		OwnerID:   group.OwnerID,
		CreatedAt: group.CreatedAt,
		UpdatedAt: group.UpdatedAt,
	}
	key := cache.GroupInfoKey(group.ID)
	if err := cacheStore.SetJSON(ctx, key, info, groupInfoCacheTTL); err != nil {
		logCacheError("SetGroupInfoCacheFailed", key, err)
	}
}

func deleteGroupInfoCache(ctx context.Context, groupID uint) {
	if groupID == 0 {
		return
	}
	key := cache.GroupInfoKey(groupID)
	if err := cacheStore.Delete(ctx, key); err != nil {
		logCacheError("DeleteGroupInfoCacheFailed", key, err)
	}
}

func (g cachedGroupInfo) toModel() *model.Group {
	return &model.Group{
		ID:        g.ID,
		Name:      g.Name,
		OwnerID:   g.OwnerID,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
	}
}
