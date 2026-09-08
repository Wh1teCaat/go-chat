package service

import (
	"context"
	"strconv"
	"testing"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"

	"gorm.io/gorm"
)

func setupPrivateConversation(t *testing.T, db *gorm.DB, user1, user2 uint) model.Conversation {
	t.Helper()
	conversation := model.Conversation{Type: model.ConversationTypePrivate}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	for _, uid := range []uint{user1, user2} {
		if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: uid}).Error; err != nil {
			t.Fatalf("failed to create membership: %v", err)
		}
	}
	return conversation
}

func TestSendConversationMessageDeduplicatesByClientMsgID(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	sender := createTestUser(t, db, "dedupe-sender@test.com")
	receiver := createTestUser(t, db, "dedupe-receiver@test.com")
	setupPrivateConversation(t, db, sender.ID, receiver.ID)

	input := dto.SendMessageInput{
		Type:        dto.WSMessageTypeMessage,
		ClientMsgID: "client-msg-1",
		TargetType:  dto.MessageTargetTypePrivate,
		TargetID:    receiver.ID,
		Content:     "hello",
	}

	first, err := MessageService.SendConversationMessage(context.Background(), sender.ID, input)
	if err != nil {
		t.Fatalf("first send returned error: %v", err)
	}
	if first.Duplicate {
		t.Fatalf("first send should not be duplicate")
	}
	if len(first.ReceiverIDs) != 1 || first.ReceiverIDs[0] != receiver.ID {
		t.Fatalf("unexpected receivers: %v", first.ReceiverIDs)
	}

	second, err := MessageService.SendConversationMessage(context.Background(), sender.ID, input)
	if err != nil {
		t.Fatalf("duplicate send returned error: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("second send should be flagged duplicate")
	}
	if second.Message.ID != first.Message.ID {
		t.Fatalf("duplicate send should return original message id %d, got %d", first.Message.ID, second.Message.ID)
	}

	var count int64
	if err := db.Model(&model.Message{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 stored message, got %d", count)
	}
}

func TestSendConversationMessageRollsBackWhenFileBindFails(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	sender := createTestUser(t, db, "tx-sender@test.com")
	receiver := createTestUser(t, db, "tx-receiver@test.com")
	setupPrivateConversation(t, db, sender.ID, receiver.ID)

	file := model.File{UserID: sender.ID, OriginalName: "a.txt", StoredName: "a", StorageKey: "k1", URL: "/u", ContentType: "text/plain", Size: 1, Purpose: "attachment"}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	// 用触发器让绑定阶段的 UPDATE 失败：GetFileByID 预检能通过，
	// 事务里消息已落库后绑定报错，验证消息会随事务回滚。
	if err := db.Exec("CREATE TRIGGER fail_bind BEFORE UPDATE OF conversation_id ON files BEGIN SELECT RAISE(ABORT, 'bind blocked'); END").Error; err != nil {
		t.Fatalf("failed to create trigger: %v", err)
	}

	content := `{"kind":"file","id":` + strconv.Itoa(int(file.ID)) + `,"filename":"a.txt"}`
	_, err := MessageService.SendConversationMessage(context.Background(), sender.ID, dto.SendMessageInput{
		Type:       dto.WSMessageTypeMessage,
		TargetType: dto.MessageTargetTypePrivate,
		TargetID:   receiver.ID,
		Content:    content,
	})
	if err == nil {
		t.Fatalf("expected send to fail when file bind fails")
	}

	var count int64
	if err := db.Model(&model.Message{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected message rollback, got %d stored messages", count)
	}
}

func TestListMessagesAfterMessageIDReturnsIncrementalAscending(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	sender := createTestUser(t, db, "after-sender@test.com")
	receiver := createTestUser(t, db, "after-receiver@test.com")
	conversation := setupPrivateConversation(t, db, sender.ID, receiver.ID)

	var ids []uint
	for _, text := range []string{"m1", "m2", "m3"} {
		msg := model.Message{ConversationID: conversation.ID, SenderID: sender.ID, Content: text}
		if err := db.Create(&msg).Error; err != nil {
			t.Fatalf("failed to create message: %v", err)
		}
		ids = append(ids, msg.ID)
	}

	messages, err := MessageService.ListMessages(context.Background(), receiver.ID, dto.ListMessagesInput{
		TargetType:     dto.MessageTargetTypePrivate,
		TargetID:       sender.ID,
		AfterMessageID: ids[0],
	})
	if err != nil {
		t.Fatalf("ListMessages returned error: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 incremental messages, got %d", len(messages))
	}
	if messages[0].ID != ids[1] || messages[1].ID != ids[2] {
		t.Fatalf("expected ascending ids %v, got %v", ids[1:], []uint{messages[0].ID, messages[1].ID})
	}
}

func TestListSessionsReturnsPrivateAndGroupWithUnread(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)

	me := createTestUser(t, db, "sessions-me@test.com")
	peer := createTestUser(t, db, "sessions-peer@test.com")
	private := setupPrivateConversation(t, db, me.ID, peer.ID)

	group := model.Group{Name: "test-group", OwnerID: peer.ID}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("failed to create group: %v", err)
	}
	groupConv := model.Conversation{Type: model.ConversationTypeGroup, GroupID: &group.ID}
	if err := db.Create(&groupConv).Error; err != nil {
		t.Fatalf("failed to create group conversation: %v", err)
	}
	for _, uid := range []uint{me.ID, peer.ID} {
		if err := db.Create(&model.ConversationMember{ConversationID: groupConv.ID, UserID: uid}).Error; err != nil {
			t.Fatalf("failed to create group membership: %v", err)
		}
	}

	// 私聊两条对方的消息（都未读），群聊一条自己的消息（不计未读）。
	for _, text := range []string{"p1", "p2"} {
		if err := db.Create(&model.Message{ConversationID: private.ID, SenderID: peer.ID, Content: text}).Error; err != nil {
			t.Fatalf("failed to create private message: %v", err)
		}
	}
	groupMsg := model.Message{ConversationID: groupConv.ID, SenderID: me.ID, Content: "g1"}
	if err := db.Create(&groupMsg).Error; err != nil {
		t.Fatalf("failed to create group message: %v", err)
	}

	sessions, err := MessageService.ListSessions(context.Background(), me.ID)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %+v", len(sessions), sessions)
	}

	byType := map[dto.MessageTargetType]dto.MessageSessionOutput{}
	for _, session := range sessions {
		byType[session.TargetType] = session
	}

	privateSession, ok := byType[dto.MessageTargetTypePrivate]
	if !ok {
		t.Fatalf("missing private session: %+v", sessions)
	}
	if privateSession.TargetID != peer.ID {
		t.Fatalf("expected private target %d, got %d", peer.ID, privateSession.TargetID)
	}
	if privateSession.UnreadCount != 2 {
		t.Fatalf("expected 2 unread in private session, got %d", privateSession.UnreadCount)
	}
	if privateSession.LastMessage == nil || privateSession.LastMessage.Content != "p2" {
		t.Fatalf("unexpected private last message: %+v", privateSession.LastMessage)
	}

	groupSession, ok := byType[dto.MessageTargetTypeGroup]
	if !ok {
		t.Fatalf("missing group session: %+v", sessions)
	}
	if groupSession.TargetID != group.ID || groupSession.Name != "test-group" {
		t.Fatalf("unexpected group session: %+v", groupSession)
	}
	if groupSession.UnreadCount != 0 {
		t.Fatalf("expected 0 unread in group session (own message), got %d", groupSession.UnreadCount)
	}
	if groupSession.LastMessage == nil || groupSession.LastMessage.ID != groupMsg.ID {
		t.Fatalf("unexpected group last message: %+v", groupSession.LastMessage)
	}
}

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
