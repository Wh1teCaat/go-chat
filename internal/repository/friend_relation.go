package repository

import (
	"context"

	"chat_proj/internal/model"
)

type FriendRelationRepository interface {
	CreateFriendRelation(ctx context.Context, relation *model.FriendRelation) error
	GetFriendRelationByID(ctx context.Context, id uint) (*model.FriendRelation, error)
	GetFriendRelationByUsers(ctx context.Context, userID, friendID uint) (*model.FriendRelation, error)
	UpdateFriendRelationStatusByID(ctx context.Context, id uint, expectedStatus, newStatus string) (bool, error)
	DeleteFriendRelation(ctx context.Context, userID, friendID uint) error
	ListFriendRelationsByUserID(ctx context.Context, userID uint, status string) ([]model.FriendRelation, error)
}

// CreateFriendRelation 创建好友关系记录。
func (r *Repository) CreateFriendRelation(ctx context.Context, relation *model.FriendRelation) error {
	return r.db.WithContext(ctx).Create(relation).Error
}

func (r *Repository) GetFriendRelationByID(ctx context.Context, id uint) (*model.FriendRelation, error) {
	var relation model.FriendRelation
	if err := r.db.WithContext(ctx).First(&relation, id).Error; err != nil {
		return nil, err
	}
	return &relation, nil
}

// GetFriendRelationByUsers 根据两个用户 ID 查询好友关系。
func (r *Repository) GetFriendRelationByUsers(ctx context.Context, userID, friendID uint) (*model.FriendRelation, error) {
	var relation model.FriendRelation
	if err := r.db.WithContext(ctx).
		Where("(user_id = ? AND friend_id = ?) OR (user_id = ? AND friend_id = ?)", userID, friendID, friendID, userID).
		First(&relation).Error; err != nil {
		return nil, err
	}
	return &relation, nil
}

func (r *Repository) UpdateFriendRelationStatusByID(ctx context.Context, id uint, expectedStatus, newStatus string) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&model.FriendRelation{}).
		Where("id = ? AND status = ?", id, expectedStatus).
		Update("status", newStatus)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListFriendRelationsByUserID 查询用户相关的好友关系。
func (r *Repository) ListFriendRelationsByUserID(ctx context.Context, userID uint, status string) ([]model.FriendRelation, error) {
	var relations []model.FriendRelation
	if err := r.db.WithContext(ctx).Where("(user_id = ? OR friend_id = ?) AND status = ?", userID, userID, status).Find(&relations).Error; err != nil {
		return nil, err
	}
	return relations, nil
}

// DeleteFriendRelation 删除两个用户之间的好友关系。
func (r *Repository) DeleteFriendRelation(ctx context.Context, userID, friendID uint) error {
	result := r.db.WithContext(ctx).
		Where("(user_id = ? AND friend_id = ?) OR (user_id = ? AND friend_id = ?)", userID, friendID, friendID, userID).
		Delete(&model.FriendRelation{})
	if result.Error != nil {
		return result.Error
	}
	return nil
}
