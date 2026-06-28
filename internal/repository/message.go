package repository

import (
	"context"
	"fmt"

	"chat_proj/internal/model"
)

type MessageRepository interface {
	CreateMessage(ctx context.Context, message *model.Message) error
	GetMessageByID(ctx context.Context, id uint) (*model.Message, error)
	ListMessagesByConversationID(ctx context.Context, conversationID, beforeMessageID uint, limit int) ([]model.Message, error)
	GetLastMessageByConversationID(ctx context.Context, conversationID uint) (*model.Message, error)
	CountUnreadMessages(ctx context.Context, conversationID, userID, lastReadMessageID uint) (int64, error)
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

// ListPotentialFileMessagesByFileID 查询可能引用某个文件 ID 的文件消息。
// content 是文本字段，这里先用 LIKE 缩小范围，业务层再解析 JSON 做精确判断。
func (r *Repository) ListPotentialFileMessagesByFileID(ctx context.Context, fileID uint) ([]model.Message, error) {
	var messages []model.Message
	err := r.db.WithContext(ctx).
		Where("content LIKE ? AND content LIKE ? AND content LIKE ?", `%"kind"%`, `%"file"%`, fmt.Sprintf("%%%d%%", fileID)).
		Find(&messages).Error
	return messages, err
}
