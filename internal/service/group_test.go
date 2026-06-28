package service

import (
	"chat_proj/internal/cache"
	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"context"
	"testing"
)

func TestCreateGroup(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	owner := createTestUser(t, db, "owner@test.com")

	err := GroupService.CreateGroup(context.Background(), "test group", owner.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	var group model.Group
	db.First(&group)
	if group.Name != "test group" || group.OwnerID != owner.ID {
		t.Error("group not created correctly")
	}

	// 应创建群会话。
	var conv model.Conversation
	db.Where("group_id = ?", group.ID).First(&conv)
	if conv.Type != model.ConversationTypeGroup {
		t.Error("conversation not created")
	}

	// 群主应同时加入群和会话。
	var gm model.GroupMember
	db.Where("group_id = ? AND user_id = ?", group.ID, owner.ID).First(&gm)
	if gm.Role != model.GroupMemberRoleOwner {
		t.Errorf("expected owner role, got %d", gm.Role)
	}
	var cm model.ConversationMember
	db.Where("conversation_id = ? AND user_id = ?", conv.ID, owner.ID).First(&cm)
}

func TestUpdateGroupInfo(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	owner := createTestUser(t, db, "owner@test.com")
	db.Create(&model.Group{Name: "old", OwnerID: owner.ID})
	db.Create(&model.GroupMember{GroupID: 1, UserID: owner.ID, Role: model.GroupMemberRoleOwner})

	t.Run("success", func(t *testing.T) {
		name := "new name"
		err := GroupService.UpdateGroupInfo(context.Background(), 1, owner.ID, dto.UpdateGroupInput{Name: &name})
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		var group model.Group
		db.First(&group, 1)
		if group.Name != "new name" {
			t.Errorf("expected 'new name', got '%s'", group.Name)
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		member := createTestUser(t, db, "member@test.com")
		db.Create(&model.GroupMember{GroupID: 1, UserID: member.ID, Role: model.GroupMemberRoleUser})
		err := GroupService.UpdateGroupInfo(context.Background(), 1, member.ID, dto.UpdateGroupInput{})
		if err == nil || err.Error() != "permission denied" {
			t.Errorf("expected permission denied, got %v", err)
		}
	})

	t.Run("operator not in group", func(t *testing.T) {
		outsider := createTestUser(t, db, "outsider@test.com")
		err := GroupService.UpdateGroupInfo(context.Background(), 1, outsider.ID, dto.UpdateGroupInput{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestTransferGroupOwner(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	owner := createTestUser(t, db, "owner@test.com")
	newOwner := createTestUser(t, db, "newowner@test.com")
	member := createTestUser(t, db, "member@test.com")
	db.Create(&model.Group{Name: "g", OwnerID: owner.ID})
	db.Create(&model.GroupMember{GroupID: 1, UserID: owner.ID, Role: model.GroupMemberRoleOwner})
	db.Create(&model.GroupMember{GroupID: 1, UserID: newOwner.ID, Role: model.GroupMemberRoleUser})
	db.Create(&model.GroupMember{GroupID: 1, UserID: member.ID, Role: model.GroupMemberRoleUser})

	t.Run("success", func(t *testing.T) {
		err := GroupService.TransferGroupOwner(context.Background(), 1, owner.ID, newOwner.ID)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		var group model.Group
		db.First(&group, 1)
		if group.OwnerID != newOwner.ID {
			t.Errorf("expected owner %d, got %d", newOwner.ID, group.OwnerID)
		}
	})

	t.Run("not owner", func(t *testing.T) {
		err := GroupService.TransferGroupOwner(context.Background(), 1, member.ID, owner.ID)
		if err == nil || err.Error() != "permission denied" {
			t.Errorf("expected permission denied, got %v", err)
		}
	})

	t.Run("self transfer", func(t *testing.T) {
		err := GroupService.TransferGroupOwner(context.Background(), 1, newOwner.ID, newOwner.ID)
		if err == nil || err.Error() != "cannot transfer ownership to self" {
			t.Errorf("expected self-transfer error, got %v", err)
		}
	})
}

func TestGroupInfoCacheAndUpdateInvalidation(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	InitCacheStore(cache.NewMemoryStore())
	defer InitCacheStore(nil)

	owner := createTestUser(t, db, "group-cache-owner@test.com")
	group := model.Group{Name: "cached-group", OwnerID: owner.ID}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("failed to create group: %v", err)
	}
	if err := db.Create(&model.GroupMember{GroupID: group.ID, UserID: owner.ID, Role: model.GroupMemberRoleOwner}).Error; err != nil {
		t.Fatalf("failed to create owner membership: %v", err)
	}

	cached, err := getGroupInfo(context.Background(), group.ID)
	if err != nil {
		t.Fatalf("getGroupInfo returned error: %v", err)
	}
	if cached.Name != "cached-group" {
		t.Fatalf("expected initial group name, got %q", cached.Name)
	}

	if err := db.Model(&model.Group{}).Where("id = ?", group.ID).Update("name", "db-only-group").Error; err != nil {
		t.Fatalf("failed to update group directly: %v", err)
	}
	cached, err = getGroupInfo(context.Background(), group.ID)
	if err != nil {
		t.Fatalf("getGroupInfo second returned error: %v", err)
	}
	if cached.Name != "cached-group" {
		t.Fatalf("expected cached group name, got %q", cached.Name)
	}

	name := "fresh-group"
	if err := GroupService.UpdateGroupInfo(context.Background(), group.ID, owner.ID, dto.UpdateGroupInput{Name: &name}); err != nil {
		t.Fatalf("UpdateGroupInfo returned error: %v", err)
	}
	cached, err = getGroupInfo(context.Background(), group.ID)
	if err != nil {
		t.Fatalf("getGroupInfo third returned error: %v", err)
	}
	if cached.Name != "fresh-group" {
		t.Fatalf("expected fresh group name after invalidation, got %q", cached.Name)
	}
}

func TestRequestJoinGroup(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	owner := createTestUser(t, db, "owner@test.com")
	user := createTestUser(t, db, "user@test.com")
	db.Create(&model.Group{Name: "g", OwnerID: owner.ID})

	request, err := GroupService.RequestJoinGroup(context.Background(), 1, user.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if request == nil || request.GroupID != 1 || request.UserID != user.ID {
		t.Fatalf("unexpected request output: %+v", request)
	}

	var req model.GroupJoinRequest
	db.Where("group_id = ? AND user_id = ?", 1, user.ID).First(&req)
	if req.Status != model.GroupJoinRequestStatusPending {
		t.Errorf("expected pending, got %s", req.Status)
	}

	if _, err := GroupService.RequestJoinGroup(context.Background(), 1, user.ID); err == nil {
		t.Fatal("expected error for duplicate request")
	}

	var count int64
	db.Model(&model.GroupJoinRequest{}).
		Where("group_id = ? AND user_id = ? AND status = ?", 1, user.ID, model.GroupJoinRequestStatusPending).
		Count(&count)
	if count != 1 {
		t.Fatalf("expected one pending join request, got %d", count)
	}
}

func TestListReviewableGroupJoinRequests(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	owner := createTestUser(t, db, "review-owner@test.com")
	admin := createTestUser(t, db, "review-admin@test.com")
	member := createTestUser(t, db, "review-member@test.com")
	outsider := createTestUser(t, db, "review-outsider@test.com")

	GroupService.CreateGroup(context.Background(), "owned", owner.ID)
	GroupService.InviteToGroup(context.Background(), 1, admin.ID, owner.ID)
	if _, err := repo.UpdateGroupMemberRole(context.Background(), 1, admin.ID, model.GroupMemberRoleAdmin); err != nil {
		t.Fatalf("failed to promote admin: %v", err)
	}

	if _, err := GroupService.RequestJoinGroup(context.Background(), 1, member.ID); err != nil {
		t.Fatalf("failed to request join: %v", err)
	}
	if _, err := GroupService.RequestJoinGroup(context.Background(), 1, outsider.ID); err != nil {
		t.Fatalf("failed to request join: %v", err)
	}

	ownerRequests, err := GroupService.ListReviewableGroupJoinRequests(context.Background(), owner.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(ownerRequests) != 2 {
		t.Fatalf("expected owner to see 2 requests, got %+v", ownerRequests)
	}

	adminRequests, err := GroupService.ListReviewableGroupJoinRequests(context.Background(), admin.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(adminRequests) != 2 {
		t.Fatalf("expected admin to see 2 requests, got %+v", adminRequests)
	}

	memberRequests, err := GroupService.ListReviewableGroupJoinRequests(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(memberRequests) != 0 {
		t.Fatalf("expected member to see no reviewable requests, got %+v", memberRequests)
	}
}

func TestInviteAndLeaveGroup(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	owner := createTestUser(t, db, "owner@test.com")
	user := createTestUser(t, db, "user@test.com")

	// 创建带会话和群主成员关系的群。
	GroupService.CreateGroup(context.Background(), "g", owner.ID)

	t.Run("invite", func(t *testing.T) {
		err := GroupService.InviteToGroup(context.Background(), 1, user.ID, owner.ID)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		var gm model.GroupMember
		db.Where("group_id = ? AND user_id = ?", 1, user.ID).First(&gm)
		if gm.Role != model.GroupMemberRoleUser {
			t.Errorf("expected member role, got %d", gm.Role)
		}
	})

	t.Run("leave", func(t *testing.T) {
		err := GroupService.LeaveGroup(context.Background(), 1, user.ID)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		var count int64
		db.Model(&model.GroupMember{}).
			Where("group_id = ? AND user_id = ?", 1, user.ID).Count(&count)
		if count != 0 {
			t.Error("user should not be in group")
		}
	})
}

func TestListGroupMembers(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	owner := createTestUser(t, db, "owner@test.com")
	member := createTestUser(t, db, "member@test.com")

	GroupService.CreateGroup(context.Background(), "g", owner.ID)
	GroupService.InviteToGroup(context.Background(), 1, member.ID, owner.ID)

	members, err := GroupService.ListGroupMembers(context.Background(), 1, owner.ID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
}

func TestUpdateGroupMemberRole(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	owner := createTestUser(t, db, "role-owner@test.com")
	admin := createTestUser(t, db, "role-admin@test.com")
	member := createTestUser(t, db, "role-member@test.com")

	GroupService.CreateGroup(context.Background(), "roles", owner.ID)
	GroupService.InviteToGroup(context.Background(), 1, admin.ID, owner.ID)
	GroupService.InviteToGroup(context.Background(), 1, member.ID, owner.ID)

	t.Run("owner promotes and demotes admin", func(t *testing.T) {
		err := GroupService.UpdateGroupMemberRole(context.Background(), 1, member.ID, owner.ID, dto.UpdateGroupMemberRoleInput{
			Role: model.GroupMemberRoleAdmin,
		})
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		role, err := repo.GetGroupMemberRole(context.Background(), 1, member.ID)
		if err != nil {
			t.Fatalf("failed to get role: %v", err)
		}
		if role != model.GroupMemberRoleAdmin {
			t.Fatalf("expected admin role, got %d", role)
		}

		err = GroupService.UpdateGroupMemberRole(context.Background(), 1, member.ID, owner.ID, dto.UpdateGroupMemberRoleInput{
			Role: model.GroupMemberRoleUser,
		})
		if err != nil {
			t.Fatalf("expected nil demoting admin, got %v", err)
		}
	})

	t.Run("admin cannot change roles", func(t *testing.T) {
		if _, err := repo.UpdateGroupMemberRole(context.Background(), 1, admin.ID, model.GroupMemberRoleAdmin); err != nil {
			t.Fatalf("failed to seed admin role: %v", err)
		}
		err := GroupService.UpdateGroupMemberRole(context.Background(), 1, member.ID, admin.ID, dto.UpdateGroupMemberRoleInput{
			Role: model.GroupMemberRoleUser,
		})
		if err == nil || err.Error() != "permission denied" {
			t.Fatalf("expected permission denied, got %v", err)
		}
	})

	t.Run("admin assigning owner role still gets permission denied", func(t *testing.T) {
		err := GroupService.UpdateGroupMemberRole(context.Background(), 1, member.ID, admin.ID, dto.UpdateGroupMemberRoleInput{
			Role: model.GroupMemberRoleOwner,
		})
		if err == nil || err.Error() != "permission denied" {
			t.Fatalf("expected permission denied, got %v", err)
		}
	})

	t.Run("owner role uses transfer endpoint", func(t *testing.T) {
		err := GroupService.UpdateGroupMemberRole(context.Background(), 1, member.ID, owner.ID, dto.UpdateGroupMemberRoleInput{
			Role: model.GroupMemberRoleOwner,
		})
		if err == nil || err.Error() != "cannot set owner role with member role API; use transfer owner API" {
			t.Fatalf("expected owner transfer error, got %v", err)
		}
	})
}
