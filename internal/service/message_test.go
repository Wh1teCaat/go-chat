package service

import (
	"context"
	"testing"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"
)

func TestMarkMessageReadDoesNotMoveLastReadBackward(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	reader := createTestUser(t, db, "reader@test.com")
	sender := createTestUser(t, db, "sender@test.com")
	conversation := model.Conversation{Type: model.ConversationTypePrivate}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: reader.ID}).Error; err != nil {
		t.Fatalf("failed to create reader membership: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: sender.ID}).Error; err != nil {
		t.Fatalf("failed to create sender membership: %v", err)
	}

	oldMessage := model.Message{ConversationID: conversation.ID, SenderID: sender.ID, Content: "old"}
	newMessage := model.Message{ConversationID: conversation.ID, SenderID: sender.ID, Content: "new"}
	if err := db.Create(&oldMessage).Error; err != nil {
		t.Fatalf("failed to create old message: %v", err)
	}
	if err := db.Create(&newMessage).Error; err != nil {
		t.Fatalf("failed to create new message: %v", err)
	}

	_, err := MessageService.MarkMessageRead(context.Background(), reader.ID, dto.MarkMessageReadInput{
		TargetType: dto.MessageTargetTypePrivate,
		TargetID:   sender.ID,
		MessageID:  newMessage.ID,
	})
	if err != nil {
		t.Fatalf("expected first mark read to succeed, got %v", err)
	}

	_, err = MessageService.MarkMessageRead(context.Background(), reader.ID, dto.MarkMessageReadInput{
		TargetType: dto.MessageTargetTypePrivate,
		TargetID:   sender.ID,
		MessageID:  oldMessage.ID,
	})
	if err != nil {
		t.Fatalf("expected stale mark read to be ignored, got %v", err)
	}

	var member model.ConversationMember
	if err := db.Where("conversation_id = ? AND user_id = ?", conversation.ID, reader.ID).First(&member).Error; err != nil {
		t.Fatalf("failed to load membership: %v", err)
	}
	if member.LastReadMessageID != newMessage.ID {
		t.Fatalf("expected last read message %d, got %d", newMessage.ID, member.LastReadMessageID)
	}
}

func TestMarkPrivateMessageReadEventTargetsReaderForReceivers(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	reader := createTestUser(t, db, "reader-event@test.com")
	sender := createTestUser(t, db, "sender-event@test.com")
	conversation := model.Conversation{Type: model.ConversationTypePrivate}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: reader.ID}).Error; err != nil {
		t.Fatalf("failed to create reader membership: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: sender.ID}).Error; err != nil {
		t.Fatalf("failed to create sender membership: %v", err)
	}

	message := model.Message{ConversationID: conversation.ID, SenderID: sender.ID, Content: "need read"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	result, err := MessageService.MarkMessageRead(context.Background(), reader.ID, dto.MarkMessageReadInput{
		TargetType: dto.MessageTargetTypePrivate,
		TargetID:   sender.ID,
		MessageID:  message.ID,
	})
	if err != nil {
		t.Fatalf("MarkMessageRead returned error: %v", err)
	}

	if result.Event.TargetID != reader.ID {
		t.Fatalf("expected private read event targetID %d for receivers, got %d", reader.ID, result.Event.TargetID)
	}
}
