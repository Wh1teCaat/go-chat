package repository

import (
	"context"

	"chat_proj/internal/model"

	"gorm.io/gorm"
)

type GroupRepository interface {
	CreateGroup(ctx context.Context, group *model.Group) error
	GetGroupByID(ctx context.Context, id uint) (*model.Group, error)
	UpdateGroup(ctx context.Context, id uint, updates map[string]interface{}) error
	ListGroupsByOwnerID(ctx context.Context, ownerID uint) ([]model.Group, error)
	ListJoinedGroupsByUserID(ctx context.Context, userID uint) ([]model.Group, error)
}

type GroupMemberRepository interface {
	AddGroupMember(ctx context.Context, member *model.GroupMember) error
	RemoveGroupMember(ctx context.Context, groupID, userID uint) error
	ListGroupMembersByGroupID(ctx context.Context, groupID uint) ([]model.GroupMember, error)
	IsUserInGroup(ctx context.Context, groupID, userID uint) (bool, error)
	GetGroupMemberRole(ctx context.Context, groupID, userID uint) (uint8, error)
	CreateGroupJoinRequest(ctx context.Context, request *model.GroupJoinRequest) error
	GetGroupJoinRequestByID(ctx context.Context, id uint) (*model.GroupJoinRequest, error)
	GetPendingGroupJoinRequest(ctx context.Context, groupID, userID uint) (*model.GroupJoinRequest, error)
	UpdateGroupJoinRequestStatus(ctx context.Context, id uint, updates map[string]interface{}) (bool, error)
	ListGroupJoinRequestsByGroupID(ctx context.Context, groupID uint) ([]model.GroupJoinRequest, error)
	ListGroupJoinRequestsByUserID(ctx context.Context, userID uint) ([]model.GroupJoinRequest, error)
	ListGroupMembersByGroupIDWithFilter(ctx context.Context, groupID uint, filter uint8) ([]model.GroupMember, error)
	ListGroupMembersByUserIDWithMinRole(ctx context.Context, userID uint, minRole uint8) ([]model.GroupMember, error)
}

// CreateGroup 创建群记录。
func (r *Repository) CreateGroup(ctx context.Context, group *model.Group) error {
	return r.db.WithContext(ctx).Create(group).Error
}

// GetGroupByID 根据 ID 查询群。
func (r *Repository) GetGroupByID(ctx context.Context, id uint) (*model.Group, error) {
	var group model.Group
	if err := r.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, err
	}
	return &group, nil
}

// UpdateGroup 只更新允许修改的群字段。
func (r *Repository) UpdateGroup(ctx context.Context, id uint, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}

	result := r.db.WithContext(ctx).
		Model(&model.Group{}).
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

// ListJoinedGroupsByUserID 查询用户加入的群。
func (r *Repository) ListJoinedGroupsByUserID(ctx context.Context, userID uint) ([]model.Group, error) {
	var groups []model.Group
	if err := r.db.WithContext(ctx).
		Table("groups").
		Select("groups.*").
		Joins("JOIN group_members ON group_members.group_id = groups.id").
		Where("group_members.user_id = ?", userID).
		Where("group_members.role <= ?", model.GroupMemberRoleAdmin). // 只列出用户以成员或管理员身份加入的群。
		Find(&groups).Error; err != nil {
		return nil, err
	}
	return groups, nil
}

func (r *Repository) ListGroupsByOwnerID(ctx context.Context, ownerID uint) ([]model.Group, error) {
	var groups []model.Group
	if err := r.db.WithContext(ctx).Where("owner_id = ?", ownerID).Find(&groups).Error; err != nil {
		return nil, err
	}
	return groups, nil
}

// AddGroupMember 添加群成员。
func (r *Repository) AddGroupMember(ctx context.Context, member *model.GroupMember) error {
	return r.db.WithContext(ctx).Create(member).Error
}

// RemoveGroupMember 移除群成员。
func (r *Repository) RemoveGroupMember(ctx context.Context, groupID, userID uint) error {
	return r.db.WithContext(ctx).Where("group_id = ? AND user_id = ?", groupID, userID).Delete(&model.GroupMember{}).Error
}

// ListGroupMembersByGroupID 查询群成员。
func (r *Repository) ListGroupMembersByGroupID(ctx context.Context, groupID uint) ([]model.GroupMember, error) {
	var members []model.GroupMember
	if err := r.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

func (r *Repository) ListGroupMembersByGroupIDWithFilter(ctx context.Context, groupID uint, filter uint8) ([]model.GroupMember, error) {
	var members []model.GroupMember
	if err := r.db.WithContext(ctx).
		Where("group_id = ? AND role >= ?", groupID, filter).
		Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

func (r *Repository) ListGroupMembersByUserIDWithMinRole(ctx context.Context, userID uint, minRole uint8) ([]model.GroupMember, error) {
	var members []model.GroupMember
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND role >= ?", userID, minRole).
		Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

// IsUserInGroup 判断用户是否在群中。
func (r *Repository) IsUserInGroup(ctx context.Context, groupID, userID uint) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.GroupMember{}).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetGroupMemberRole 查询用户在群中的角色。
func (r *Repository) GetGroupMemberRole(ctx context.Context, groupID, userID uint) (uint8, error) {
	var member model.GroupMember
	if err := r.db.WithContext(ctx).
		Select("role").
		Where("group_id = ? AND user_id = ?", groupID, userID).
		First(&member).Error; err != nil {
		return 0, err
	}
	return member.Role, nil
}

func (r *Repository) UpdateGroupMemberRole(ctx context.Context, groupID, userID uint, newRole uint8) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&model.GroupMember{}).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Update("role", newRole)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// CreateGroupJoinRequest 创建入群申请。
func (r *Repository) CreateGroupJoinRequest(ctx context.Context, request *model.GroupJoinRequest) error {
	return r.db.WithContext(ctx).Create(request).Error
}

// GetGroupJoinRequestByID 根据 ID 查询入群申请。
func (r *Repository) GetGroupJoinRequestByID(ctx context.Context, id uint) (*model.GroupJoinRequest, error) {
	var request model.GroupJoinRequest
	if err := r.db.WithContext(ctx).First(&request, id).Error; err != nil {
		return nil, err
	}
	return &request, nil
}

// GetPendingGroupJoinRequest 查询用户在某个群中的待处理入群申请。
func (r *Repository) GetPendingGroupJoinRequest(ctx context.Context, groupID, userID uint) (*model.GroupJoinRequest, error) {
	var request model.GroupJoinRequest
	if err := r.db.WithContext(ctx).
		Where("group_id = ? AND user_id = ? AND status = ?", groupID, userID, "pending").
		First(&request).Error; err != nil {
		return nil, err
	}
	return &request, nil
}

// UpdateGroupJoinRequestStatus 更新入群申请字段。
func (r *Repository) UpdateGroupJoinRequestStatus(ctx context.Context, id uint, updates map[string]interface{}) (bool, error) {
	if len(updates) == 0 {
		return false, nil
	}
	result := r.db.WithContext(ctx).
		Model(&model.GroupJoinRequest{}).
		Where("id = ? AND status = ?", id, model.GroupJoinRequestStatusPending).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListGroupJoinRequestsByGroupID 查询某个群的入群申请。
func (r *Repository) ListGroupJoinRequestsByGroupID(ctx context.Context, groupID uint) ([]model.GroupJoinRequest, error) {
	var requests []model.GroupJoinRequest
	if err := r.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&requests).Error; err != nil {
		return nil, err
	}
	return requests, nil
}

// ListGroupJoinRequestsByUserID 查询用户发起的入群申请。
func (r *Repository) ListGroupJoinRequestsByUserID(ctx context.Context, userID uint) ([]model.GroupJoinRequest, error) {
	var requests []model.GroupJoinRequest
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&requests).Error; err != nil {
		return nil, err
	}
	return requests, nil
}
