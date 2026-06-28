package service

import (
	"context"
	"os"
	"testing"

	"chat_proj/internal/cache"
	"chat_proj/internal/config"
	"chat_proj/internal/model"
)

func TestUserProfileCacheRealRedis(t *testing.T) {
	if os.Getenv("CHAT_REDIS_INTEGRATION") != "1" {
		t.Skip("set CHAT_REDIS_INTEGRATION=1 to run real redis integration test")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if !cfg.Redis.Enabled {
		t.Fatal("redis is disabled by config")
	}

	client, err := cache.NewRedisClient(context.Background(), cfg.Redis)
	if err != nil {
		t.Fatalf("NewRedisClient returned error: %v", err)
	}
	defer client.Close()

	db := setupTestDB(t)
	initRepo(db)
	InitCacheStore(cache.NewRedisStore(client))
	defer InitCacheStore(nil)

	owner := createTestUser(t, db, "redis-cache-owner@test.com")
	friend := createTestUser(t, db, "redis-cache-friend@test.com")
	friend.Nickname = "redis-cached-name"
	if err := db.Save(friend).Error; err != nil {
		t.Fatalf("failed to update friend: %v", err)
	}
	if err := db.Create(&model.FriendRelation{
		UserID:   owner.ID,
		FriendID: friend.ID,
		Status:   model.FriendRelationStatusAccepted,
	}).Error; err != nil {
		t.Fatalf("failed to create friend relation: %v", err)
	}

	key := cache.UserProfileKey(friend.ID)
	defer client.Del(context.Background(), key)

	if err := client.Del(context.Background(), key).Err(); err != nil {
		t.Fatalf("failed to cleanup redis key: %v", err)
	}

	friends, err := UserService.ListFriends(context.Background(), owner.ID)
	if err != nil {
		t.Fatalf("ListFriends returned error: %v", err)
	}
	if len(friends) != 1 || friends[0].Nickname != "redis-cached-name" {
		t.Fatalf("unexpected friends before cache assertion: %+v", friends)
	}

	exists, err := client.Exists(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("redis EXISTS returned error: %v", err)
	}
	if exists != 1 {
		t.Fatalf("expected redis key %q to exist", key)
	}

	if err := db.Model(&model.User{}).Where("id = ?", friend.ID).Update("nickname", "db-only-name").Error; err != nil {
		t.Fatalf("failed to update friend directly: %v", err)
	}

	friends, err = UserService.ListFriends(context.Background(), owner.ID)
	if err != nil {
		t.Fatalf("ListFriends second returned error: %v", err)
	}
	if len(friends) != 1 || friends[0].Nickname != "redis-cached-name" {
		t.Fatalf("expected redis cached profile, got %+v", friends)
	}
}
