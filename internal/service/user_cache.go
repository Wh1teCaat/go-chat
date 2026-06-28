package service

import (
	"context"
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

func getUserProfile(ctx context.Context, userID uint) (*model.User, error) {
	if userID == 0 {
		return repo.GetUserByID(ctx, userID)
	}

	var cached cachedUserProfile
	key := cache.UserProfileKey(userID)
	ok, err := cacheStore.GetJSON(ctx, key, &cached)
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
		ok, err := cacheStore.GetJSON(ctx, key, &cached)
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
	if err := cacheStore.SetJSON(ctx, key, profile, userProfileCacheTTL); err != nil {
		logCacheError("SetUserProfileCacheFailed", key, err)
	}
}

func deleteUserProfileCache(ctx context.Context, userID uint) {
	if userID == 0 {
		return
	}
	key := cache.UserProfileKey(userID)
	if err := cacheStore.Delete(ctx, key); err != nil {
		logCacheError("DeleteUserProfileCacheFailed", key, err)
	}
}

func (p cachedUserProfile) toModel() *model.User {
	return &model.User{
		ID:       p.ID,
		Email:    p.Email,
		Nickname: p.Nickname,
		Avatar:   p.Avatar,
	}
}

func logCacheError(message, key string, err error) {
	logger.Warn(message,
		logger.String("key", key),
		logger.String("error", err.Error()),
	)
}
