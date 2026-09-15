package repository

import (
	"context"
	"time"

	"chat_proj/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ConversationRepository interface {
	CreateConversation(ctx context.Context, conversation *model.Conversation) error
	GetConversationByID(ctx context.Context, id uint) (*model.Conversation, error)
	GetConversationForUpdate(ctx context.Context, id uint) (*model.Conversation, error)
	ReserveNextConversationSeqOnly(ctx context.Context, id uint) (uint64, error)
	ReserveNextConversationSeq(ctx context.Context, id uint) (seq, publishedSeq uint64, err error)
	AdvanceConversationPublishedSeq(ctx context.Context, id uint, previous, next uint64) error
	AdvanceConversationPublishedSeqAtLeast(ctx context.Context, id uint, next uint64) error
	GetConversationPublishedSeq(ctx context.Context, id uint) (uint64, error)
	ListConversationIDsWithUnpublishedMessages(ctx context.Context, staleBefore time.Time, limit int) ([]uint, error)
	HasUnpublishedMessages(ctx context.Context, id uint) (bool, error)
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

// GetConversationForUpdate 锁定会话行，供消息序号分配和有序发布协调使用。
// PostgreSQL 会在事务结束前保持行锁；SQLite 测试环境会忽略该锁语义。
func (r *Repository) GetConversationForUpdate(ctx context.Context, id uint) (*model.Conversation, error) {
	var conversation model.Conversation
	if err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&conversation, id).Error; err != nil {
		return nil, err
	}
	return &conversation, nil
}

// ReserveNextConversationSeq 在当前事务内为会话保留下一个连续序号。
// UPDATE 同时充当该会话的跨实例串行化点；不同会话不会互相等待。
func (r *Repository) ReserveNextConversationSeq(ctx context.Context, id uint) (seq, publishedSeq uint64, err error) {
	seq, err = r.ReserveNextConversationSeqOnly(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	publishedSeq, err = r.GetConversationPublishedSeq(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	return seq, publishedSeq, nil
}

// ReserveNextConversationSeqOnly is used by Kafka consumers, whose partition
// order removes the need to read the legacy Redis publication watermark.
func (r *Repository) ReserveNextConversationSeqOnly(ctx context.Context, id uint) (uint64, error) {
	var reserved struct {
		LastSeq uint64
	}
	result := r.db.WithContext(ctx).
		Raw("UPDATE conversations SET last_seq = last_seq + 1 WHERE id = ? RETURNING last_seq", id).
		Scan(&reserved)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, gorm.ErrRecordNotFound
	}
	return reserved.LastSeq, nil
}

func (r *Repository) GetConversationPublishedSeq(ctx context.Context, id uint) (uint64, error) {
	var watermark model.ConversationPublishWatermark
	if err := r.db.WithContext(ctx).First(&watermark, "conversation_id = ?", id).Error; err != nil {
		return 0, err
	}
	return watermark.LastPublishedSeq, nil
}

// AdvanceConversationPublishedSeq 推进已经成功交给总线的连续水位。
// previous 条件避免意外跳过尚未发布的序号。
func (r *Repository) AdvanceConversationPublishedSeq(ctx context.Context, id uint, previous, next uint64) error {
	result := r.db.WithContext(ctx).
		Model(&model.ConversationPublishWatermark{}).
		Where("conversation_id = ? AND last_published_seq = ?", id, previous).
		UpdateColumn("last_published_seq", next)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// AdvanceConversationPublishedSeqAtLeast 确认 Redis 序号门已经连续发布到 next。
// next 由 Lua 脚本的实际返回值提供，所以允许一次跨越同批被释放的多个序号。
func (r *Repository) AdvanceConversationPublishedSeqAtLeast(ctx context.Context, id uint, next uint64) error {
	return r.db.WithContext(ctx).
		Model(&model.ConversationPublishWatermark{}).
		Where("conversation_id = ? AND last_published_seq < ?", id, next).
		UpdateColumn("last_published_seq", r.db.Raw("CASE WHEN last_published_seq < ? THEN ? ELSE last_published_seq END", next, next)).Error
}

// ListConversationIDsWithUnpublishedMessages 供后台恢复任务扫描提交后未成功发布的消息。
func (r *Repository) ListConversationIDsWithUnpublishedMessages(ctx context.Context, staleBefore time.Time, limit int) ([]uint, error) {
	var ids []uint
	query := r.db.WithContext(ctx).
		Table("conversations c").
		Joins("JOIN conversation_publish_watermarks w ON w.conversation_id = c.id").
		Joins("JOIN messages m ON m.conversation_id = c.id AND m.seq = w.last_published_seq + 1").
		Where("c.last_seq > w.last_published_seq AND m.created_at < ?", staleBefore).
		Order("c.id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Pluck("c.id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// HasUnpublishedMessages 在 publisher 完成一轮 drain 后检查是否有新提交消息落在本轮锁之后。
func (r *Repository) HasUnpublishedMessages(ctx context.Context, id uint) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Table("conversations c").
		Joins("JOIN conversation_publish_watermarks w ON w.conversation_id = c.id").
		Where("c.id = ? AND c.last_seq > w.last_published_seq", id).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
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
