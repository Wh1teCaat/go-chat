package service

import (
	"chat_proj/internal/cache"
	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	presencepkg "chat_proj/internal/presence"
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestRegister(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	t.Run("success", func(t *testing.T) {
		err := UserService.Register(context.Background(), dto.RegisterUserInput{
			Email:    "test@example.com",
			Password: "password123",
		})
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}

		var user model.User
		if err := db.Where("email = ?", "test@example.com").First(&user).Error; err != nil {
			t.Fatalf("user not created: %v", err)
		}
		if user.Email != "test@example.com" {
			t.Errorf("expected email test@example.com, got %s", user.Email)
		}
		if user.Password == "password123" {
			t.Error("password should be hashed")
		}
	})

	t.Run("empty email or password", func(t *testing.T) {
		err := UserService.Register(context.Background(), dto.RegisterUserInput{
			Email:    "",
			Password: "password",
		})
		if err == nil {
			t.Fatal("expected error for empty email")
		}
	})

	t.Run("duplicate email", func(t *testing.T) {
		input := dto.RegisterUserInput{Email: "dup@example.com", Password: "password"}
		_ = UserService.Register(context.Background(), input)
		err := UserService.Register(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for duplicate email")
		}
	})
}

func TestLogin(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	hashed, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.DefaultCost)
	db.Create(&model.User{Email: "login@test.com", Password: string(hashed)})

	t.Run("success", func(t *testing.T) {
		user, err := UserService.Login(context.Background(), dto.LoginUserInput{
			Email:    "login@test.com",
			Password: "correct",
		})
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if user.Email != "login@test.com" {
			t.Fatalf("expected login@test.com, got %s", user.Email)
		}
		if user.UserID == 0 {
			t.Fatal("expected user ID")
		}
	})

	t.Run("invalid email", func(t *testing.T) {
		_, err := UserService.Login(context.Background(), dto.LoginUserInput{
			Email:    "no@test.com",
			Password: "correct",
		})
		if err == nil {
			t.Fatal("expected error for invalid email")
		}
		if err.Error() != "invalid email or password" {
			t.Errorf("expected 'invalid email or password', got '%s'", err.Error())
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		_, err := UserService.Login(context.Background(), dto.LoginUserInput{
			Email:    "login@test.com",
			Password: "wrong",
		})
		if err == nil {
			t.Fatal("expected error for wrong password")
		}
		if err.Error() != "invalid email or password" {
			t.Errorf("expected 'invalid email or password', got '%s'", err.Error())
		}
	})
}

func TestUpdateUserInfo(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	user := createTestUser(t, db, "update@test.com")

	t.Run("success", func(t *testing.T) {
		nickname := "newname"
		err := UserService.UpdateUserInfo(context.Background(), user.ID, dto.UpdateUserInput{
			Nickname: &nickname,
		})
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}

		var updated model.User
		db.First(&updated, user.ID)
		if updated.Nickname != "newname" {
			t.Errorf("expected nickname 'newname', got '%s'", updated.Nickname)
		}
	})

	t.Run("no fields to update", func(t *testing.T) {
		err := UserService.UpdateUserInfo(context.Background(), user.ID, dto.UpdateUserInput{})
		if err == nil {
			t.Fatal("expected error for empty updates")
		}
	})
}

func TestAddFriendByEmail(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	user1 := createTestUser(t, db, "email-sender@test.com")
	user2 := createTestUser(t, db, "email-target@test.com")

	t.Run("success", func(t *testing.T) {
		result, err := UserService.AddFriendByEmail(context.Background(), user1.ID, user2.Email)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if result == nil {
			t.Fatal("expected friend request result")
		}
		if result.ReceiverID != user2.ID {
			t.Fatalf("expected receiver %d, got %d", user2.ID, result.ReceiverID)
		}
		if result.Request.UserID != user1.ID {
			t.Fatalf("expected requester %d, got %d", user1.ID, result.Request.UserID)
		}
		if result.Request.RequesterEmail != user1.Email {
			t.Fatalf("expected requester email %q, got %q", user1.Email, result.Request.RequesterEmail)
		}

		var rel model.FriendRelation
		db.Where("user_id = ? AND friend_id = ?", user1.ID, user2.ID).First(&rel)
		if rel.Status != model.FriendRelationStatusPending {
			t.Errorf("expected status pending, got %s", rel.Status)
		}
	})

	t.Run("target not found", func(t *testing.T) {
		_, err := UserService.AddFriendByEmail(context.Background(), user1.ID, "missing@test.com")
		if err == nil {
			t.Fatal("expected error for missing email")
		}
	})

	t.Run("add self", func(t *testing.T) {
		_, err := UserService.AddFriendByEmail(context.Background(), user1.ID, user1.Email)
		if err == nil {
			t.Fatal("expected error for self-add")
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		_, err := UserService.AddFriendByEmail(context.Background(), user1.ID, user2.Email)
		if err == nil {
			t.Fatal("expected error for duplicate relation")
		}
	})

	t.Run("reverse duplicate", func(t *testing.T) {
		_, err := UserService.AddFriendByEmail(context.Background(), user2.ID, user1.Email)
		if err == nil {
			t.Fatal("expected error for reverse duplicate relation")
		}

		var count int64
		db.Model(&model.FriendRelation{}).
			Where("(user_id = ? AND friend_id = ?) OR (user_id = ? AND friend_id = ?)", user1.ID, user2.ID, user2.ID, user1.ID).
			Count(&count)
		if count != 1 {
			t.Fatalf("expected one relation between users, got %d", count)
		}
	})
}

func TestAcceptFriend(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	user1 := createTestUser(t, db, "sender@test.com")
	user2 := createTestUser(t, db, "receiver@test.com")
	other := createTestUser(t, db, "other@test.com")

	db.Create(&model.FriendRelation{
		UserID: other.ID, FriendID: user1.ID,
		Status: model.FriendRelationStatusRejected,
	})

	t.Run("success", func(t *testing.T) {
		relation := model.FriendRelation{
			UserID: user1.ID, FriendID: user2.ID,
			Status: model.FriendRelationStatusPending,
		}
		db.Create(&relation)

		if relation.ID == user1.ID {
			t.Fatalf("test setup failed: request id should differ from requester id")
		}

		err := UserService.AcceptFriend(context.Background(), user2.ID, relation.ID)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}

		var rel model.FriendRelation
		db.Where("user_id = ? AND friend_id = ?", user1.ID, user2.ID).First(&rel)
		if rel.Status != model.FriendRelationStatusAccepted {
			t.Errorf("expected accepted, got %s", rel.Status)
		}
	})

	t.Run("already accepted", func(t *testing.T) {
		var rel model.FriendRelation
		db.Where("user_id = ? AND friend_id = ?", user1.ID, user2.ID).First(&rel)
		err := UserService.AcceptFriend(context.Background(), user2.ID, rel.ID)
		if err == nil {
			t.Fatal("expected error for non-pending relation")
		}
	})

	t.Run("not found", func(t *testing.T) {
		err := UserService.AcceptFriend(context.Background(), user1.ID, 9999)
		if err == nil {
			t.Fatal("expected error for non-existent relation")
		}
	})
}

func TestRejectFriend(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	user1 := createTestUser(t, db, "a@test.com")
	user2 := createTestUser(t, db, "b@test.com")
	other := createTestUser(t, db, "c@test.com")

	db.Create(&model.FriendRelation{
		UserID: other.ID, FriendID: user1.ID,
		Status: model.FriendRelationStatusRejected,
	})

	t.Run("success", func(t *testing.T) {
		relation := model.FriendRelation{
			UserID: user1.ID, FriendID: user2.ID,
			Status: model.FriendRelationStatusPending,
		}
		db.Create(&relation)

		if relation.ID == user1.ID {
			t.Fatalf("test setup failed: request id should differ from requester id")
		}

		err := UserService.RejectFriend(context.Background(), user2.ID, relation.ID)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}

		var rel model.FriendRelation
		db.Where("user_id = ? AND friend_id = ?", user1.ID, user2.ID).First(&rel)
		if rel.Status != model.FriendRelationStatusRejected {
			t.Errorf("expected rejected, got %s", rel.Status)
		}
	})

	t.Run("not found", func(t *testing.T) {
		err := UserService.RejectFriend(context.Background(), user1.ID, 9999)
		if err == nil {
			t.Fatal("expected error for non-existent relation")
		}
	})
}

func TestRemoveFriend(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	user1 := createTestUser(t, db, "x@test.com")
	user2 := createTestUser(t, db, "y@test.com")

	t.Run("success", func(t *testing.T) {
		db.Create(&model.FriendRelation{
			UserID: user1.ID, FriendID: user2.ID, Status: "accepted",
		})

		err := UserService.RemoveFriend(context.Background(), user1.ID, user2.ID)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}

		var count int64
		db.Model(&model.FriendRelation{}).
			Where("user_id = ? AND friend_id = ?", user1.ID, user2.ID).
			Count(&count)
		if count != 0 {
			t.Error("relation should be deleted")
		}
	})

	t.Run("not found", func(t *testing.T) {
		err := UserService.RemoveFriend(context.Background(), user1.ID, 9999)
		if err == nil {
			t.Fatal("expected error for non-existent relation")
		}
	})
}

func TestListFriends(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	presenceStore := presencepkg.NewMemoryStore()
	InitPresenceStore(presenceStore)
	defer InitPresenceStore(nil)

	user1 := createTestUser(t, db, "me@test.com")
	user2 := createTestUser(t, db, "f1@test.com")
	user3 := createTestUser(t, db, "f2@test.com")

	// 已通过的好友关系。
	db.Create(&model.FriendRelation{
		UserID: user1.ID, FriendID: user2.ID, Status: model.FriendRelationStatusAccepted,
	})
	// 待处理关系不应出现在好友列表中。
	db.Create(&model.FriendRelation{
		UserID: user1.ID, FriendID: user3.ID, Status: model.FriendRelationStatusPending,
	})

	friends, err := UserService.ListFriends(context.Background(), user1.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(friends) != 1 {
		t.Fatalf("expected 1 friend, got %d", len(friends))
	}
	if friends[0].Status != model.FriendRelationStatusAccepted {
		t.Errorf("expected accepted, got %s", friends[0].Status)
	}
	if friends[0].UserID != user2.ID {
		t.Errorf("expected friend user %d, got %d", user2.ID, friends[0].UserID)
	}
	if friends[0].Online {
		t.Fatal("expected friend to be offline before websocket connection")
	}

	if err := presenceStore.Connect(context.Background(), user2.ID, "friend-conn"); err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	friends, err = UserService.ListFriends(context.Background(), user1.ID)
	if err != nil {
		t.Fatalf("ListFriends after presence returned error: %v", err)
	}
	if !friends[0].Online {
		t.Fatal("expected friend to be online after presence connect")
	}
}

func TestUserProfileCacheAndUpdateInvalidation(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	InitCacheStore(cache.NewMemoryStore())
	defer InitCacheStore(nil)

	user1 := createTestUser(t, db, "cache-owner@test.com")
	user2 := createTestUser(t, db, "cache-friend@test.com")
	user2.Nickname = "cached-name"
	user2.Avatar = "/uploads/avatar-a.png"
	if err := db.Save(user2).Error; err != nil {
		t.Fatalf("failed to update test friend: %v", err)
	}
	if err := db.Create(&model.FriendRelation{
		UserID:   user1.ID,
		FriendID: user2.ID,
		Status:   model.FriendRelationStatusAccepted,
	}).Error; err != nil {
		t.Fatalf("failed to create friend relation: %v", err)
	}

	friends, err := UserService.ListFriends(context.Background(), user1.ID)
	if err != nil {
		t.Fatalf("ListFriends returned error: %v", err)
	}
	if len(friends) != 1 || friends[0].Nickname != "cached-name" {
		t.Fatalf("expected cached-name before cache test, got %+v", friends)
	}

	if err := db.Model(&model.User{}).Where("id = ?", user2.ID).Update("nickname", "db-only-name").Error; err != nil {
		t.Fatalf("failed to update friend directly: %v", err)
	}
	friends, err = UserService.ListFriends(context.Background(), user1.ID)
	if err != nil {
		t.Fatalf("ListFriends second returned error: %v", err)
	}
	if len(friends) != 1 || friends[0].Nickname != "cached-name" {
		t.Fatalf("expected cached friend profile, got %+v", friends)
	}

	nickname := "fresh-name"
	if err := UserService.UpdateUserInfo(context.Background(), user2.ID, dto.UpdateUserInput{Nickname: &nickname}); err != nil {
		t.Fatalf("UpdateUserInfo returned error: %v", err)
	}
	friends, err = UserService.ListFriends(context.Background(), user1.ID)
	if err != nil {
		t.Fatalf("ListFriends third returned error: %v", err)
	}
	if len(friends) != 1 || friends[0].Nickname != "fresh-name" {
		t.Fatalf("expected fresh profile after cache invalidation, got %+v", friends)
	}
}

func TestListPendingFriendRequests(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	userA := createTestUser(t, db, "a@test.com") // receiver
	userB := createTestUser(t, db, "b@test.com") // sender

	// B 向 A 发起好友申请，A 是接收方。
	relation := model.FriendRelation{
		UserID: userB.ID, FriendID: userA.ID, Status: model.FriendRelationStatusPending,
	}
	db.Create(&relation)

	requests, err := UserService.ListPendingFriendRequests(context.Background(), userA.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].RequestID != relation.ID {
		t.Errorf("expected request ID %d, got %d", relation.ID, requests[0].RequestID)
	}
	if requests[0].RequesterEmail != userB.Email {
		t.Errorf("expected sender email %s, got %s", userB.Email, requests[0].RequesterEmail)
	}
}
