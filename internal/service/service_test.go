package service

import (
	"chat_proj/internal/model"
	"chat_proj/internal/repository"
	"chat_proj/pkg/logger"
	"os"
	"testing"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	logger.Logger = zap.NewNop()
	os.Exit(m.Run())
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.Group{},
		&model.GroupMember{},
		&model.Conversation{},
		&model.ConversationMember{},
		&model.Message{},
		&model.File{},
		&model.UploadSession{},
		&model.UploadChunk{},
		&model.FriendRelation{},
		&model.GroupJoinRequest{},
	); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}

	return db
}

func initRepo(db *gorm.DB) {
	Init(repository.NewRepository(db))
	InitCacheStore(nil)
}

func createTestUser(t *testing.T, db *gorm.DB, email string) *model.User {
	t.Helper()
	user := &model.User{Email: email, Password: "hashed", Nickname: "test"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user
}
