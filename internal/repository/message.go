package repository

import (
	"context"
	"fmt"

	"chat_proj/internal/model"
)

type MessageRepository interface {
	CreateMessage(ctx context.Context, message *model.Message) error
	GetMessageByID(ctx context.Context, id uint) (*model.Message, error)
	GetMessageBySenderAndClientMsgID(ctx context.Context, senderID uint, clientMsgID string) (*model.Message, error)
	ListMessagesByConversationID(ctx context.Context, conversationID, beforeMessageID uint, limit int) ([]model.Message, error)
	ListMessagesAfterMessageID(ctx context.Context, conversationID, afterMessageID uint, limit int) ([]model.Message, error)
	GetLastMessageByConversationID(ctx context.Context, conversationID uint) (*model.Message, error)
	GetLastMessagesByConversationIDs(ctx context.Context, conversationIDs []uint) ([]model.Message, error)
	CountUnreadMessages(ctx context.Context, conversationID, userID, lastReadMessageID uint) (int64, error)
	CountUnreadMessagesByConversationIDs(ctx context.Context, userID uint, conversationIDs []uint) (map[uint]int64, error)
	ListPotentialFileMessagesByFileID(ctx context.Context, fileID uint) ([]model.Message, error)
}

// CreateMessage 创建消息记录。
func (r *Repository) CreateMessage(ctx context.Context, message *model.Message) error {
	return r.db.WithContext(ctx).Create(message).Error
}

// GetMessageByID 根据 ID 查询消息。
func (r *Repository) GetMessageByID(ctx context.Context, id uint) (*model.Message, error) {
	var message model.Message
	if err := r.db.WithContext(ctx).First(&message, id).Error; err != nil {
		return nil, err
	}
	return &message, nil
}

// GetMessageBySenderAndClientMsgID 按 (sender, clientMsgID) 查已落库的消息，用于发送幂等去重。
func (r *Repository) GetMessageBySenderAndClientMsgID(ctx context.Context, senderID uint, clientMsgID string) (*model.Message, error) {
	var message model.Message
	if err := r.db.WithContext(ctx).
		Where("sender_id = ? AND client_msg_id = ?", senderID, clientMsgID).
		First(&message).Error; err != nil {
		return nil, err
	}
	return &message, nil
}

// ListMessagesByConversationID 按最新消息优先返回；beforeMessageID 存在时返回更早的消息。
func (r *Repository) ListMessagesByConversationID(ctx context.Context, conversationID, beforeMessageID uint, limit int) ([]model.Message, error) {
	var messages []model.Message
	query := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("id DESC")
	if beforeMessageID > 0 {
		query = query.Where("id < ?", beforeMessageID)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&messages).Error; err != nil {
		return nil, err
	}
	return messages, nil
}

// ListMessagesAfterMessageID 按 id 升序返回比 afterMessageID 更新的消息，用于断线重连后的增量补拉。
func (r *Repository) ListMessagesAfterMessageID(ctx context.Context, conversationID, afterMessageID uint, limit int) ([]model.Message, error) {
	var messages []model.Message
	query := r.db.WithContext(ctx).
		Where("conversation_id = ? AND id > ?", conversationID, afterMessageID).
		Order("id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&messages).Error; err != nil {
		return nil, err
	}
	return messages, nil
}

func (r *Repository) GetLastMessageByConversationID(ctx context.Context, conversationID uint) (*model.Message, error) {
	var message model.Message
	if err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("id DESC").
		First(&message).Error; err != nil {
		return nil, err
	}
	return &message, nil
}

func (r *Repository) CountUnreadMessages(ctx context.Context, conversationID, userID, lastReadMessageID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.Message{}).
		Where("conversation_id = ? AND id > ? AND sender_id <> ?", conversationID, lastReadMessageID, userID).
		Count(&count).Error
	return count, err
}

// GetLastMessagesByConversationIDs 一次取出每个会话的最后一条消息。
// 子查询取每个会话的 MAX(id)，在 PostgreSQL 和 SQLite（测试）下行为一致。
func (r *Repository) GetLastMessagesByConversationIDs(ctx context.Context, conversationIDs []uint) ([]model.Message, error) {
	if len(conversationIDs) == 0 {
		return nil, nil
	}
	var messages []model.Message
	sub := r.db.Model(&model.Message{}).
		Select("MAX(id)").
		Where("conversation_id IN ?", conversationIDs).
		Group("conversation_id")
	err := r.db.WithContext(ctx).Where("id IN (?)", sub).Find(&messages).Error
	return messages, err
}

// CountUnreadMessagesByConversationIDs 一条 SQL 算出用户在多个会话中的未读数，
// 每个会话按该用户自己的 last_read_message_id 截断。
func (r *Repository) CountUnreadMessagesByConversationIDs(ctx context.Context, userID uint, conversationIDs []uint) (map[uint]int64, error) {
	result := make(map[uint]int64, len(conversationIDs))
	if len(conversationIDs) == 0 {
		return result, nil
	}
	rows := []struct {
		ConversationID uint
		Count          int64
	}{}
	err := r.db.WithContext(ctx).
		Table("messages").
		Select("messages.conversation_id AS conversation_id, COUNT(*) AS count").
		Joins("JOIN conversation_members cm ON cm.conversation_id = messages.conversation_id AND cm.user_id = ?", userID).
		Where("messages.conversation_id IN ?", conversationIDs).
		Where("messages.id > cm.last_read_message_id AND messages.sender_id <> ?", userID).
		Group("messages.conversation_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ConversationID] = row.Count
	}
	return result, nil
}

// ListPotentialFileMessagesByFileID 查询可能引用某个文件 ID 的文件消息。
// content 是文本字段，这里先用 LIKE 缩小范围，业务层再解析 JSON 做精确判断。
func (r *Repository) ListPotentialFileMessagesByFileID(ctx context.Context, fileID uint) ([]model.Message, error) {
	var messages []model.Message
	err := r.db.WithContext(ctx).
		Where("content LIKE ? AND content LIKE ? AND content LIKE ?", `%"kind"%`, `%"file"%`, fmt.Sprintf("%%%d%%", fileID)).
		Find(&messages).Error
	return messages, err
}
