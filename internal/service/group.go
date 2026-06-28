package service

import (
	"context"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"chat_proj/internal/repository"
	"chat_proj/pkg/apperrors"
)

type groupService struct{}

var GroupService = new(groupService)

// CreateGroup 在一个事务中创建群、群会话，并把群主加入群和会话。
func (g *groupService) CreateGroup(ctx context.Context, name string, ownerID uint) error {
	return repo.WithTransaction(func(txRepo *repository.Repository) error {
		group := &model.Group{
			Name:    name,
			OwnerID: ownerID,
		}
		if err := txRepo.CreateGroup(ctx, group); err != nil {
			return dbOperationError(err)
		}

		conversation := &model.Conversation{
			Type:    model.ConversationTypeGroup,
			GroupID: &group.ID,
		}
		if err := txRepo.CreateConversation(ctx, conversation); err != nil {
			return dbOperationError(err)
		}

		if err := txRepo.AddGroupMember(ctx, &model.GroupMember{
			GroupID: group.ID,
			UserID:  ownerID,
			Role:    model.GroupMemberRoleOwner,
		}); err != nil {
			return dbOperationError(err)
		}

		if err := txRepo.AddConversationMember(ctx, &model.ConversationMember{
			ConversationID: conversation.ID,
			UserID:         ownerID,
		}); err != nil {
			return dbOperationError(err)
		}

		return nil
	})
}

// UpdateGroupInfo 只更新允许修改的群字段。
func (g *groupService) UpdateGroupInfo(ctx context.Context, groupID, operatorID uint, input dto.UpdateGroupInput) error {
	role, err := repo.GetGroupMemberRole(ctx, groupID, operatorID)
	if err != nil {
		return dbOperationError(err)
	}

	if role != model.GroupMemberRoleAdmin && role != model.GroupMemberRoleOwner {
		return apperrors.ErrPermissionDenied
	}

	updates := map[string]interface{}{}
	if input.Name != nil {
		updates["name"] = *input.Name
	}
	if err := repo.UpdateGroup(ctx, groupID, updates); err != nil {
		return dbOperationError(err)
	}
	if len(updates) > 0 {
		deleteGroupInfoCache(ctx, groupID)
	}
	return nil
}

// TransferGroupOwner 把群主身份转让给另一个成员。
func (g *groupService) TransferGroupOwner(ctx context.Context, groupID, fromUserID, toUserID uint) error {
	if fromUserID == toUserID {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "cannot transfer ownership to self")
	}

	fromRole, err := repo.GetGroupMemberRole(ctx, groupID, fromUserID)
	if err != nil {
		return dbOperationError(err)
	}
	if fromRole != model.GroupMemberRoleOwner {
		return apperrors.ErrPermissionDenied
	}

	if inGroup, err := repo.IsUserInGroup(ctx, groupID, toUserID); err != nil {
		return dbOperationError(err)
	} else if !inGroup {
		return apperrors.WithMessage(apperrors.ErrNotFound, "target user is not a member of the group")
	}

	err = repo.WithTransaction(func(txRepo *repository.Repository) error {
		if err := txRepo.UpdateGroup(ctx, groupID, map[string]interface{}{"owner_id": toUserID}); err != nil {
			return dbOperationError(err)
		}
		if _, err := txRepo.UpdateGroupMemberRole(ctx, groupID, fromUserID, model.GroupMemberRoleAdmin); err != nil {
			return dbOperationError(err)
		}
		if _, err := txRepo.UpdateGroupMemberRole(ctx, groupID, toUserID, model.GroupMemberRoleOwner); err != nil {
			return dbOperationError(err)
		}
		return nil
	})
	if err == nil {
		deleteGroupInfoCache(ctx, groupID)
	}
	return err
}

// ListMyGroups 查询当前用户创建的群。
func (g *groupService) ListMyGroups(ctx context.Context, userID uint) ([]dto.GroupOutput, error) {
	groups, err := repo.ListGroupsByOwnerID(ctx, userID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return toGroupOutputs(groups), nil
}

// ListMyJoinedGroups 查询当前用户加入的群。
func (g *groupService) ListMyJoinedGroups(ctx context.Context, userID uint) ([]dto.GroupOutput, error) {
	groups, err := repo.ListJoinedGroupsByUserID(ctx, userID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return toGroupOutputs(groups), nil
}

// -- 群成员相关方法 ------------------------------------------------------------

func (g *groupService) addMemberToGroupAndConversation(ctx context.Context, txRepo *repository.Repository, groupID, userID uint) error {
	conversation, err := txRepo.GetConversationByGroupID(ctx, groupID)
	if err != nil {
		return dbOperationError(err)
	}

	if err := txRepo.AddGroupMember(ctx, &model.GroupMember{
		GroupID: groupID,
		UserID:  userID,
		Role:    model.GroupMemberRoleUser,
	}); err != nil {
		return dbOperationError(err)
	}

	if err := txRepo.AddConversationMember(ctx, &model.ConversationMember{
		ConversationID: conversation.ID,
		UserID:         userID,
	}); err != nil {
		return dbOperationError(err)
	}
	return nil
}

func (g *groupService) RequestJoinGroup(ctx context.Context, groupID, userID uint) (*dto.GroupJoinRequestOutput, error) {
	var output *dto.GroupJoinRequestOutput
	err := repo.WithTransaction(func(txRepo *repository.Repository) error {
		if _, err := txRepo.GetGroupByID(ctx, groupID); err != nil {
			return apperrors.WithMessage(apperrors.ErrNotFound, "group not found")
		}
		if ok, err := txRepo.IsUserInGroup(ctx, groupID, userID); err != nil {
			return dbOperationError(err)
		} else if ok {
			return nil
		}
		if _, err := txRepo.GetPendingGroupJoinRequest(ctx, groupID, userID); err == nil {
			return apperrors.WithMessage(apperrors.ErrConflict, "join request already pending")
		}

		request := &model.GroupJoinRequest{
			GroupID: groupID,
			UserID:  userID,
			Status:  model.GroupJoinRequestStatusPending,
		}
		if err := txRepo.CreateGroupJoinRequest(ctx, request); err != nil {
			return dbOperationError(err)
		}
		dtoOutput := toGroupJoinRequestOutput(*request)
		output = &dtoOutput
		return nil
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (g *groupService) ReviewGroupJoinRequest(ctx context.Context, requestID uint, input dto.GroupJoinRequestReviewInput) error {
	if input.Status != model.GroupJoinRequestStatusApproved && input.Status != model.GroupJoinRequestStatusRejected {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid review status")
	}

	request, err := repo.GetGroupJoinRequestByID(ctx, requestID)
	if err != nil {
		return apperrors.WithMessage(apperrors.ErrNotFound, "group join request not found")
	}
	if request.Status != model.GroupJoinRequestStatusPending {
		return apperrors.WithMessage(apperrors.ErrConflict, "request already processed")
	}

	reviewerRole, err := repo.GetGroupMemberRole(ctx, request.GroupID, input.ReviewerID)
	if err != nil {
		return dbOperationError(err)
	}
	if reviewerRole != model.GroupMemberRoleAdmin && reviewerRole != model.GroupMemberRoleOwner {
		return apperrors.ErrPermissionDenied
	}

	return repo.WithTransaction(func(txRepo *repository.Repository) error {
		updated, err := txRepo.UpdateGroupJoinRequestStatus(ctx, requestID, map[string]interface{}{
			"status":      input.Status,
			"reviewed_by": input.ReviewerID,
		})
		if err != nil {
			return dbOperationError(err)
		}
		if !updated {
			return apperrors.WithMessage(apperrors.ErrConflict, "request already processed")
		}

		if input.Status == model.GroupJoinRequestStatusApproved {
			return g.addMemberToGroupAndConversation(ctx, txRepo, request.GroupID, request.UserID)
		}
		return nil
	})
}

func (g *groupService) ListGroupJoinRequests(ctx context.Context, groupID, operatorID uint) ([]dto.GroupJoinRequestOutput, error) {
	role, err := repo.GetGroupMemberRole(ctx, groupID, operatorID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	if role != model.GroupMemberRoleAdmin && role != model.GroupMemberRoleOwner {
		return nil, apperrors.ErrPermissionDenied
	}
	requests, err := repo.ListGroupJoinRequestsByGroupID(ctx, groupID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return toGroupJoinRequestOutputs(requests), nil
}

func (g *groupService) ListMyGroupJoinRequests(ctx context.Context, userID uint) ([]dto.GroupJoinRequestOutput, error) {
	requests, err := repo.ListGroupJoinRequestsByUserID(ctx, userID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return toGroupJoinRequestOutputs(requests), nil
}

func (g *groupService) ListReviewableGroupJoinRequests(ctx context.Context, userID uint) ([]dto.GroupJoinRequestOutput, error) {
	memberships, err := repo.ListGroupMembersByUserIDWithMinRole(ctx, userID, model.GroupMemberRoleAdmin)
	if err != nil {
		return nil, dbOperationError(err)
	}

	result := make([]dto.GroupJoinRequestOutput, 0)
	for _, membership := range memberships {
		requests, err := repo.ListGroupJoinRequestsByGroupID(ctx, membership.GroupID)
		if err != nil {
			return nil, dbOperationError(err)
		}
		for _, request := range requests {
			if request.Status == model.GroupJoinRequestStatusPending {
				result = append(result, toGroupJoinRequestOutput(request))
			}
		}
	}
	return result, nil
}

func (g *groupService) ListGroupApproverIDs(ctx context.Context, groupID uint) ([]uint, error) {
	members, err := repo.ListGroupMembersByGroupIDWithFilter(ctx, groupID, model.GroupMemberRoleAdmin)
	if err != nil {
		return nil, dbOperationError(err)
	}
	ids := make([]uint, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.UserID)
	}
	return ids, nil
}

func (g *groupService) InviteToGroup(ctx context.Context, groupID, userID, operatorID uint) error {
	if _, err := getGroupInfo(ctx, groupID); err != nil {
		return apperrors.WithMessage(apperrors.ErrNotFound, "group not found")
	}
	if ok, err := repo.IsUserInGroup(ctx, groupID, userID); err != nil {
		return dbOperationError(err)
	} else if ok {
		return nil
	}

	role, err := repo.GetGroupMemberRole(ctx, groupID, operatorID)
	if err != nil {
		return dbOperationError(err)
	}
	if role != model.GroupMemberRoleAdmin && role != model.GroupMemberRoleOwner {
		return apperrors.ErrPermissionDenied
	}

	return repo.WithTransaction(func(txRepo *repository.Repository) error {
		return g.addMemberToGroupAndConversation(ctx, txRepo, groupID, userID)
	})
}

func (g *groupService) removeMemberFromGroupAndConversation(ctx context.Context, groupID, userID uint) error {
	return repo.WithTransaction(func(txRepo *repository.Repository) error {
		conversation, err := txRepo.GetConversationByGroupID(ctx, groupID)
		if err != nil {
			return dbOperationError(err)
		}

		if err := txRepo.RemoveGroupMember(ctx, groupID, userID); err != nil {
			return dbOperationError(err)
		}

		if err := txRepo.RemoveConversationMember(ctx, conversation.ID, userID); err != nil {
			return dbOperationError(err)
		}
		return nil
	})
}

func (g *groupService) LeaveGroup(ctx context.Context, groupID, userID uint) error {
	inGroup, err := repo.IsUserInGroup(ctx, groupID, userID)
	if err != nil {
		return dbOperationError(err)
	}
	if !inGroup {
		return apperrors.WithMessage(apperrors.ErrNotFound, "user is not a member of the group")
	}

	role, err := repo.GetGroupMemberRole(ctx, groupID, userID)
	if err != nil {
		return dbOperationError(err)
	}
	if role == model.GroupMemberRoleOwner {
		return apperrors.WithMessage(apperrors.ErrConflict, "group owner cannot leave directly, pls transfer ownership")
	}
	return g.removeMemberFromGroupAndConversation(ctx, groupID, userID)
}

func (g *groupService) RemoveGroupMember(ctx context.Context, groupID, userID, operatorID uint) error {
	userInGroup, err1 := repo.IsUserInGroup(ctx, groupID, userID)
	if err1 != nil {
		return dbOperationError(err1)
	}
	operatorInGroup, err2 := repo.IsUserInGroup(ctx, groupID, operatorID)
	if err2 != nil {
		return dbOperationError(err2)
	}
	if !userInGroup || !operatorInGroup {
		return apperrors.WithMessage(apperrors.ErrNotFound, "user or operator is not a member of the group")
	}

	operatorRole, err := repo.GetGroupMemberRole(ctx, groupID, operatorID)
	if err != nil {
		return dbOperationError(err)
	}
	userRole, err := repo.GetGroupMemberRole(ctx, groupID, userID)
	if err != nil {
		return dbOperationError(err)
	}
	if operatorRole <= userRole {
		return apperrors.WithMessage(apperrors.ErrPermissionDenied, "cannot remove a member with equal or higher role")
	}

	return g.removeMemberFromGroupAndConversation(ctx, groupID, userID)
}

func (g *groupService) UpdateGroupMemberRole(ctx context.Context, groupID, userID, operatorID uint, input dto.UpdateGroupMemberRoleInput) error {
	operatorRole, err := repo.GetGroupMemberRole(ctx, groupID, operatorID)
	if err != nil {
		return dbOperationError(err)
	}
	if operatorRole != model.GroupMemberRoleOwner {
		return apperrors.ErrPermissionDenied
	}

	if userID == operatorID {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "cannot change own role")
	}
	if input.Role == model.GroupMemberRoleOwner {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "cannot set owner role with member role API; use transfer owner API")
	}

	targetRole, err := repo.GetGroupMemberRole(ctx, groupID, userID)
	if err != nil {
		return dbOperationError(err)
	}
	if targetRole >= operatorRole {
		return apperrors.WithMessage(apperrors.ErrPermissionDenied, "cannot change role of a member with equal or higher role")
	}
	updated, err := repo.UpdateGroupMemberRole(ctx, groupID, userID, input.Role)
	if err != nil {
		return dbOperationError(err)
	}
	if !updated {
		return apperrors.WithMessage(apperrors.ErrNotFound, "group member not found")
	}
	return nil
}

func (g *groupService) ListGroupMembers(ctx context.Context, groupID, userID uint) ([]dto.GroupMemberOutput, error) {
	inGroup, err := repo.IsUserInGroup(ctx, groupID, userID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	if !inGroup {
		return nil, apperrors.ErrPermissionDenied
	}
	members, err := repo.ListGroupMembersByGroupID(ctx, groupID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return toGroupMemberOutputs(members), nil
}

func toGroupOutput(group model.Group) dto.GroupOutput {
	return dto.GroupOutput{
		ID:        group.ID,
		Name:      group.Name,
		OwnerID:   group.OwnerID,
		CreatedAt: formatMessageTime(group.CreatedAt),
		UpdatedAt: formatMessageTime(group.UpdatedAt),
	}
}

func toGroupOutputs(groups []model.Group) []dto.GroupOutput {
	result := make([]dto.GroupOutput, 0, len(groups))
	for _, group := range groups {
		result = append(result, toGroupOutput(group))
	}
	return result
}

func toGroupMemberOutput(member model.GroupMember) dto.GroupMemberOutput {
	return dto.GroupMemberOutput{
		UserID:   member.UserID,
		Role:     member.Role,
		JoinedAt: formatMessageTime(member.CreatedAt),
	}
}

func toGroupMemberOutputs(members []model.GroupMember) []dto.GroupMemberOutput {
	result := make([]dto.GroupMemberOutput, 0, len(members))
	for _, member := range members {
		result = append(result, toGroupMemberOutput(member))
	}
	return result
}

func toGroupJoinRequestOutput(request model.GroupJoinRequest) dto.GroupJoinRequestOutput {
	return dto.GroupJoinRequestOutput{
		ID:         request.ID,
		GroupID:    request.GroupID,
		UserID:     request.UserID,
		Status:     request.Status,
		ReviewedBy: request.ReviewedBy,
		ReviewedAt: formatOptionalTime(request.ReviewedAt),
		CreatedAt:  formatMessageTime(request.CreatedAt),
		UpdatedAt:  formatMessageTime(request.UpdatedAt),
	}
}

func toGroupJoinRequestOutputs(requests []model.GroupJoinRequest) []dto.GroupJoinRequestOutput {
	result := make([]dto.GroupJoinRequestOutput, 0, len(requests))
	for _, request := range requests {
		result = append(result, toGroupJoinRequestOutput(request))
	}
	return result
}
