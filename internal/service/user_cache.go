package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"chat_proj/internal/cache"
	"chat_proj/internal/model"
	"chat_proj/pkg/logger"
)

const userProfileCacheTTL = 10 * time.Minute

type cachedUserProfile struct {
	ID       uint   `json:"id"`
	Email    string `json:"email"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// getUserProfile 优先从缓存读取用户资料，未命中时查询数据库并回填缓存。
func getUserProfile(ctx context.Context, userID uint) (*model.User, error) {
	if userID == 0 {
		return repo.GetUserByID(ctx, userID)
	}

	var cached cachedUserProfile
	key := cache.UserProfileKey(userID)
	fields, ok, err := cacheStore.GetHash(ctx, key)
	if err == nil && ok {
		cached, err = decodeUserProfile(fields)
	}
	if err != nil {
		logCacheError("GetUserProfileCacheFailed", key, err)
	} else if ok {
		return cached.toModel(), nil
	}

	user, err := repo.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	setUserProfileCache(ctx, *user)
	return user, nil
}

// getUserProfilesByIDs 批量读取用户资料，并只为缓存未命中的用户查询数据库。
func getUserProfilesByIDs(ctx context.Context, ids []uint) ([]model.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	users := make([]model.User, 0, len(ids))
	missing := make([]uint, 0, len(ids))
	seen := map[uint]struct{}{}

	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		key := cache.UserProfileKey(id)
		var cached cachedUserProfile
		fields, ok, err := cacheStore.GetHash(ctx, key)
		if err == nil && ok {
			cached, err = decodeUserProfile(fields)
		}
		if err != nil {
			logCacheError("GetUserProfileCacheFailed", key, err)
			missing = append(missing, id)
			continue
		}
		if ok {
			users = append(users, *cached.toModel())
			continue
		}
		missing = append(missing, id)
	}

	if len(missing) == 0 {
		return users, nil
	}

	dbUsers, err := repo.GetUsersByIDs(ctx, missing)
	if err != nil {
		return nil, err
	}
	for _, user := range dbUsers {
		setUserProfileCache(ctx, user)
		users = append(users, user)
	}
	return users, nil
}

// setUserProfileCache 将用户资料写入缓存并记录非致命缓存错误。
func setUserProfileCache(ctx context.Context, user model.User) {
	if user.ID == 0 {
		return
	}
	// 用户资料缓存只保存展示字段，不能把 password hash 放入 Redis。
	profile := cachedUserProfile{
		ID:       user.ID,
		Email:    user.Email,
		Nickname: user.Nickname,
		Avatar:   user.Avatar,
	}
	key := cache.UserProfileKey(user.ID)
	if err := cacheStore.SetHash(ctx, key, profile.toHash(), userProfileCacheTTL); err != nil {
		logCacheError("SetUserProfileCacheFailed", key, err)
	}
}

// deleteUserProfileCache 删除指定用户的资料缓存。
func deleteUserProfileCache(ctx context.Context, userID uint) {
	if userID == 0 {
		return
	}
	key := cache.UserProfileKey(userID)
	if err := cacheStore.Delete(ctx, key); err != nil {
		logCacheError("DeleteUserProfileCacheFailed", key, err)
	}
}

// toModel 将缓存中的用户资料转换为领域模型。
func (p cachedUserProfile) toModel() *model.User {
	return &model.User{
		ID:       p.ID,
		Email:    p.Email,
		Nickname: p.Nickname,
		Avatar:   p.Avatar,
	}
}

// logCacheError 以统一字段记录不影响主流程的缓存错误。
func logCacheError(message, key string, err error) {
	logger.Warn(message,
		logger.String("key", key),
		logger.String("error", err.Error()),
	)
}

// toHash 显式编码允许缓存的资料字段。
func (p cachedUserProfile) toHash() map[string]string {
	return map[string]string{"id": strconv.FormatUint(uint64(p.ID), 10), "email": p.Email, "nickname": p.Nickname, "avatar": p.Avatar}
}

// decodeUserProfile 校验字段并还原资料快照。
func decodeUserProfile(m map[string]string) (cachedUserProfile, error) {
	for _, key := range []string{"id", "email", "nickname", "avatar"} {
		if _, ok := m[key]; !ok {
			return cachedUserProfile{}, fmt.Errorf("missing cache field %s", key)
		}
	}
	id, err := strconv.ParseUint(m["id"], 10, strconv.IntSize)
	if err != nil {
		return cachedUserProfile{}, err
	}
	return cachedUserProfile{ID: uint(id), Email: m["email"], Nickname: m["nickname"], Avatar: m["avatar"]}, nil
}
