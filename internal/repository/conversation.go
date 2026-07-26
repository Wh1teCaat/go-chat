package repository

import (
	"context"

	"chat_proj/internal/model"
)

type ConversationRepository interface {
	CreateConversation(ctx context.Context, conversation *model.Conversation) error
	GetConversationByID(ctx context.Context, id uint) (*model.Conversation, error)
	GetPrivateConversationBetweenUsers(ctx context.Context, user1ID, user2ID uint) (*model.Conversation, error)
	GetConversationByGroupID(ctx context.Context, groupID uint) (*model.Conversation, error)
	ListConversationsByUserID(ctx context.Context, userID uint) ([]model.Conversation, error)
}

type ConversationMemberRepository interface {
	AddConversationMember(ctx context.Context, member *model.ConversationMember) error
	RemoveConversationMember(ctx context.Context, conversationID, userID uint) error
	ListConversationMembersByConversationID(ctx context.Context, conversationID uint) ([]model.ConversationMember, error)
	ListConversationMembersByConversationIDs(ctx context.Context, conversationIDs []uint) ([]model.ConversationMember, error)
	ListConversationMembersByUserID(ctx context.Context, userID uint) ([]model.ConversationMember, error)
	IsUserInConversation(ctx context.Context, conversationID, userID uint) (bool, error)
	UpdateConversationMemberLastReadMessageID(ctx context.Context, conversationID, userID, messageID uint) error
}

// CreateConversation 创建会话记录。
func (r *Repository) CreateConversation(ctx context.Context, conversation *model.Conversation) error {
	return r.db.WithContext(ctx).Create(conversation).Error
}

// GetConversationByID 根据 ID 查询会话。
func (r *Repository) GetConversationByID(ctx context.Context, id uint) (*model.Conversation, error) {
	var conversation model.Conversation
	if err := r.db.WithContext(ctx).First(&conversation, id).Error; err != nil {
		return nil, err
	}
	return &conversation, nil
}

// GetPrivateConversationBetweenUsers 查询两个用户之间的单聊会话。
func (r *Repository) GetPrivateConversationBetweenUsers(ctx context.Context, user1ID, user2ID uint) (*model.Conversation, error) {
	var conversation model.Conversation
	err := r.db.WithContext(ctx).
		Table("conversations").
		Select("conversations.*").
		Joins("JOIN conversation_members cm1 ON cm1.conversation_id = conversations.id").
		Joins("JOIN conversation_members cm2 ON cm2.conversation_id = conversations.id").
		Where("conversations.type = ?", model.ConversationTypePrivate).
		Where("cm1.user_id = ? AND cm2.user_id = ?", user1ID, user2ID).
		First(&conversation).Error
	if err != nil {
		return nil, err
	}
	return &conversation, nil
}

// GetConversationByGroupID 根据群 ID 查询群聊会话。
func (r *Repository) GetConversationByGroupID(ctx context.Context, groupID uint) (*model.Conversation, error) {
	var conversation model.Conversation
	if err := r.db.WithContext(ctx).Where("type = ? AND group_id = ?", model.ConversationTypeGroup, groupID).First(&conversation).Error; err != nil {
		return nil, err
	}
	return &conversation, nil
}

// ListConversationsByUserID 查询用户所属的所有会话。
func (r *Repository) ListConversationsByUserID(ctx context.Context, userID uint) ([]model.Conversation, error) {
	var conversations []model.Conversation
	if err := r.db.WithContext(ctx).
		Table("conversations").
		Select("conversations.*").
		Joins("JOIN conversation_members cm ON cm.conversation_id = conversations.id").
		Where("cm.user_id = ?", userID).
		Find(&conversations).Error; err != nil {
		return nil, err
	}
	return conversations, nil
}

// AddConversationMember 添加会话成员。
func (r *Repository) AddConversationMember(ctx context.Context, member *model.ConversationMember) error {
	return r.db.WithContext(ctx).Create(member).Error
}

// RemoveConversationMember 移除会话成员。
func (r *Repository) RemoveConversationMember(ctx context.Context, conversationID, userID uint) error {
	return r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Delete(&model.ConversationMember{}).Error
}

// ListConversationMembersByConversationID 查询会话成员。
func (r *Repository) ListConversationMembersByConversationID(ctx context.Context, conversationID uint) ([]model.ConversationMember, error) {
	var members []model.ConversationMember
	if err := r.db.WithContext(ctx).Where("conversation_id = ?", conversationID).Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

// ListConversationMembersByConversationIDs 批量查询多个会话的成员，会话列表用它避免逐会话查询。
func (r *Repository) ListConversationMembersByConversationIDs(ctx context.Context, conversationIDs []uint) ([]model.ConversationMember, error) {
	if len(conversationIDs) == 0 {
		return nil, nil
	}
	var members []model.ConversationMember
	if err := r.db.WithContext(ctx).Where("conversation_id IN ?", conversationIDs).Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

// ListConversationMembersByUserID 查询用户自己的全部会话成员记录（含 last_read_message_id）。
func (r *Repository) ListConversationMembersByUserID(ctx context.Context, userID uint) ([]model.ConversationMember, error) {
	var members []model.ConversationMember
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

// IsUserInConversation 判断用户是否属于某个会话。
func (r *Repository) IsUserInConversation(ctx context.Context, conversationID, userID uint) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateConversationMemberLastReadMessageID 更新会话成员的最后已读消息 ID。
func (r *Repository) UpdateConversationMemberLastReadMessageID(ctx context.Context, conversationID, userID, messageID uint) error {
	return r.db.WithContext(ctx).
		Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ? AND last_read_message_id < ?", conversationID, userID, messageID).
		Update("last_read_message_id", messageID).Error
}
