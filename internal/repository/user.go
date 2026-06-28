package repository

import (
	"context"

	"chat_proj/internal/model"

	"gorm.io/gorm"
)

type UserRepository interface {
	CreateUser(ctx context.Context, user *model.User) error
	GetUserByID(ctx context.Context, id uint) (*model.User, error)
	GetUsersByIDs(ctx context.Context, ids []uint) ([]model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	GetUserIDByEmail(ctx context.Context, email string) (uint, error)
	UpdateUser(ctx context.Context, id uint, updates map[string]interface{}) error
}

// CreateUser 创建用户记录。
func (r *Repository) CreateUser(ctx context.Context, user *model.User) error {
	return r.db.WithContext(ctx).Create(user).Error
}

// GetUserByID 根据 ID 查询用户。
func (r *Repository) GetUserByID(ctx context.Context, id uint) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUsersByIDs 根据 ID 列表批量查询用户。
func (r *Repository) GetUsersByIDs(ctx context.Context, ids []uint) ([]model.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var users []model.User
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// GetUserByEmail 根据邮箱查询用户。
func (r *Repository) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).First(&user, "email = ?", email).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) GetUserIDByEmail(ctx context.Context, email string) (uint, error) {
	var user model.User
	if err := r.db.WithContext(ctx).
		Select("id").
		First(&user, "email = ?", email).Error; err != nil {
		return 0, err
	}
	return user.ID, nil
}

// UpdateUser 只更新允许修改的用户资料字段。
func (r *Repository) UpdateUser(ctx context.Context, id uint, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}

	result := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", id).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
