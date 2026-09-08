package service

import (
	"context"
	"fmt"
	"strconv"
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

// getGroupInfo 优先从缓存读取群组信息，未命中时查询数据库并回填缓存。
func getGroupInfo(ctx context.Context, groupID uint) (*model.Group, error) {
	if groupID == 0 {
		return repo.GetGroupByID(ctx, groupID)
	}

	key := cache.GroupInfoKey(groupID)
	var cached cachedGroupInfo
	fields, ok, err := cacheStore.GetHash(ctx, key)
	if err == nil && ok {
		cached, err = decodeGroupInfo(fields)
	}
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

// setGroupInfoCache 将群组信息写入缓存并记录非致命缓存错误。
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
	if err := cacheStore.SetHash(ctx, key, info.toHash(), groupInfoCacheTTL); err != nil {
		logCacheError("SetGroupInfoCacheFailed", key, err)
	}
}

// deleteGroupInfoCache 删除指定群组的信息缓存。
func deleteGroupInfoCache(ctx context.Context, groupID uint) {
	if groupID == 0 {
		return
	}
	key := cache.GroupInfoKey(groupID)
	if err := cacheStore.Delete(ctx, key); err != nil {
		logCacheError("DeleteGroupInfoCacheFailed", key, err)
	}
}

// toModel 将缓存中的群组信息转换为领域模型。
func (g cachedGroupInfo) toModel() *model.Group {
	return &model.Group{
		ID:        g.ID,
		Name:      g.Name,
		OwnerID:   g.OwnerID,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
	}
}

// toHash 显式编码群组信息，时间使用 RFC3339Nano。
func (g cachedGroupInfo) toHash() map[string]string {
	return map[string]string{"id": strconv.FormatUint(uint64(g.ID), 10), "name": g.Name, "ownerID": strconv.FormatUint(uint64(g.OwnerID), 10), "createdAt": g.CreatedAt.Format(time.RFC3339Nano), "updatedAt": g.UpdatedAt.Format(time.RFC3339Nano)}
}

// decodeGroupInfo 校验字段并还原群组快照。
func decodeGroupInfo(m map[string]string) (cachedGroupInfo, error) {
	for _, key := range []string{"id", "name", "ownerID", "createdAt", "updatedAt"} {
		if _, ok := m[key]; !ok {
			return cachedGroupInfo{}, fmt.Errorf("missing cache field %s", key)
		}
	}
	id, err := strconv.ParseUint(m["id"], 10, strconv.IntSize)
	if err != nil {
		return cachedGroupInfo{}, err
	}
	owner, err := strconv.ParseUint(m["ownerID"], 10, strconv.IntSize)
	if err != nil {
		return cachedGroupInfo{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, m["createdAt"])
	if err != nil {
		return cachedGroupInfo{}, err
	}
	updated, err := time.Parse(time.RFC3339Nano, m["updatedAt"])
	if err != nil {
		return cachedGroupInfo{}, err
	}
	return cachedGroupInfo{ID: uint(id), Name: m["name"], OwnerID: uint(owner), CreatedAt: created, UpdatedAt: updated}, nil
}
