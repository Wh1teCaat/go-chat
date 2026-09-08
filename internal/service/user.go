package service

import (
	"context"
	"errors"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"chat_proj/internal/repository"
	"chat_proj/pkg/apperrors"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type userService struct{}

var UserService = new(userService)

type FriendRequestResult struct {
	ReceiverID uint
	Request    dto.PendingFriendRequestOutput
}

// Register 校验注册信息、加密密码并创建用户。
func (u *userService) Register(ctx context.Context, input dto.RegisterUserInput) error {
	if input.Email == "" || input.Password == "" {
		return apperrors.ErrEmptyFields
	}

	if _, err := repo.GetUserIDByEmail(ctx, input.Email); err == nil {
		return apperrors.WithMessage(apperrors.ErrEmailAlreadyExists, "email already registered")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return dbOperationError(err)
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return apperrors.WithCause(apperrors.ErrHashFailed, "password hashing failed", err)
	}

	user := &model.User{
		Email:    input.Email,
		Password: string(hashedPassword),
	}

	if input.Nickname != nil {
		user.Nickname = *input.Nickname
	}
	if input.Avatar != nil {
		user.Avatar = *input.Avatar
	}

	if err := repo.CreateUser(ctx, user); err != nil {
		return dbOperationError(err)
	}
	return nil
}

// Login 校验邮箱和密码并返回用户身份信息。
func (u *userService) Login(ctx context.Context, input dto.LoginUserInput) (*dto.LoginUserOutput, error) {
	if input.Email == "" || input.Password == "" {
		return nil, apperrors.ErrEmptyFields
	}

	user, err := repo.GetUserByEmail(ctx, input.Email)
	if err != nil {
		return nil, apperrors.WithMessage(apperrors.ErrWrongPassword, "invalid email or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
		return nil, apperrors.WithMessage(apperrors.ErrWrongPassword, "invalid email or password")
	}

	return &dto.LoginUserOutput{
		UserID: user.ID,
		Email:  user.Email,
	}, nil
}

// UpdateUserInfo 更新用户允许修改的资料并清除资料缓存。
func (u *userService) UpdateUserInfo(ctx context.Context, id uint, input dto.UpdateUserInput) error {
	updates := map[string]interface{}{}
	if input.Nickname != nil {
		updates["nickname"] = *input.Nickname
	}
	if input.Avatar != nil {
		updates["avatar"] = *input.Avatar
	}
	if len(updates) == 0 {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "no fields to update")
	}
	if err := repo.UpdateUser(ctx, id, updates); err != nil {
		return dbOperationError(err)
	}
	deleteUserProfileCache(ctx, id)
	return nil
}

// -- 好友关系相关方法 ----------------------------------------------------------

// AddFriendByEmail 根据邮箱创建好友申请，并处理反向申请自动接受逻辑。
func (u *userService) AddFriendByEmail(ctx context.Context, userID uint, friendEmail string) (*FriendRequestResult, error) {
	if friendEmail == "" {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "friendEmail is required")
	}
	friendID, err := repo.GetUserIDByEmail(ctx, friendEmail)
	if err != nil {
		return nil, apperrors.WithMessage(apperrors.ErrUserNotFound, "target user not found")
	}
	if userID == friendID {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "cannot add self as friend")
	}
	if _, err := repo.GetFriendRelationByUsers(ctx, userID, friendID); err == nil {
		return nil, apperrors.WithMessage(apperrors.ErrConflict, "friend relation already exists")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, dbOperationError(err)
	}
	relation := &model.FriendRelation{
		UserID:   userID,
		FriendID: friendID,
		Status:   model.FriendRelationStatusPending,
	}
	if err := repo.CreateFriendRelation(ctx, relation); err != nil {
		return nil, dbOperationError(err)
	}
	requester, err := getUserProfile(ctx, userID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return &FriendRequestResult{
		ReceiverID: friendID,
		Request:    toPendingFriendRequestOutput(*relation, *requester),
	}, nil
}

// AcceptFriend 接受发给当前用户的待处理好友申请并建立私聊会话。
func (u *userService) AcceptFriend(ctx context.Context, userID, requestID uint) error {
	relation, err := repo.GetFriendRelationByID(ctx, requestID)
	if err != nil {
		return apperrors.WithMessage(apperrors.ErrNotFound, "friend request not found")
	}
	if relation.FriendID != userID {
		return apperrors.ErrPermissionDenied
	}

	return repo.WithTransaction(func(txRepo *repository.Repository) error {
		updated, err := txRepo.UpdateFriendRelationStatusByID(ctx, requestID,
			model.FriendRelationStatusPending, model.FriendRelationStatusAccepted)
		if err != nil {
			return dbOperationError(err)
		}
		if !updated {
			return apperrors.WithMessage(apperrors.ErrConflict, "friend request is not pending or not found")
		}

		if _, err := txRepo.GetPrivateConversationBetweenUsers(ctx, relation.UserID, relation.FriendID); err == nil {
			return nil
		}

		conversation := &model.Conversation{Type: model.ConversationTypePrivate}
		if err := txRepo.CreateConversation(ctx, conversation); err != nil {
			return dbOperationError(err)
		}
		if err := txRepo.AddConversationMember(ctx, &model.ConversationMember{
			ConversationID: conversation.ID,
			UserID:         relation.UserID,
		}); err != nil {
			return dbOperationError(err)
		}
		if err := txRepo.AddConversationMember(ctx, &model.ConversationMember{
			ConversationID: conversation.ID,
			UserID:         relation.FriendID,
		}); err != nil {
			return dbOperationError(err)
		}
		return nil
	})
}

// RejectFriend 拒绝发给当前用户的待处理好友申请。
func (u *userService) RejectFriend(ctx context.Context, userID, requestID uint) error {
	relation, err := repo.GetFriendRelationByID(ctx, requestID)
	if err != nil {
		return apperrors.WithMessage(apperrors.ErrNotFound, "friend request not found")
	}
	if relation.FriendID != userID {
		return apperrors.ErrPermissionDenied
	}
	updated, err := repo.UpdateFriendRelationStatusByID(ctx, requestID,
		model.FriendRelationStatusPending, model.FriendRelationStatusRejected)
	if err != nil {
		return dbOperationError(err)
	}
	if !updated {
		return apperrors.WithMessage(apperrors.ErrConflict, "friend request is not pending or not found")
	}
	return nil
}

// RemoveFriend 删除两个用户之间的好友关系。
func (u *userService) RemoveFriend(ctx context.Context, userID, friendID uint) error {
	if _, err := repo.GetFriendRelationByUsers(ctx, userID, friendID); err != nil {
		return apperrors.WithMessage(apperrors.ErrNotFound, "friend relation not found")
	}
	return repo.WithTransaction(func(txRepo *repository.Repository) error {
		conversation, err := txRepo.GetPrivateConversationBetweenUsers(ctx, userID, friendID)
		if err == nil {
			if err := txRepo.RemoveConversationMember(ctx, conversation.ID, userID); err != nil {
				return dbOperationError(err)
			}
			if err := txRepo.RemoveConversationMember(ctx, conversation.ID, friendID); err != nil {
				return dbOperationError(err)
			}
		}
		if err := txRepo.DeleteFriendRelation(ctx, userID, friendID); err != nil {
			return dbOperationError(err)
		}
		return nil
	})
}

// ListFriends 返回用户的好友资料及当前在线状态。
func (u *userService) ListFriends(ctx context.Context, userID uint) ([]dto.FriendOutput, error) {
	all, err := repo.ListFriendRelationsByUserID(ctx, userID, model.FriendRelationStatusAccepted)
	if err != nil {
		return nil, dbOperationError(err)
	}
	if len(all) == 0 {
		return nil, nil
	}

	ids := make([]uint, len(all))
	for i, r := range all {
		ids[i] = relationPeerID(r, userID)
	}

	users, err := getUserProfilesByIDs(ctx, ids)
	if err != nil {
		return nil, dbOperationError(err)
	}
	userMap := make(map[uint]model.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}

	friends := make([]dto.FriendOutput, 0, len(ids))
	onlineByID := listOnlineStatuses(ctx, ids)
	for _, fid := range ids {
		user, ok := userMap[fid]
		if !ok {
			continue
		}
		friends = append(friends, dto.FriendOutput{
			UserID:   user.ID,
			Nickname: user.Nickname,
			Avatar:   user.Avatar,
			Status:   model.FriendRelationStatusAccepted,
			Online:   onlineByID[fid],
		})
	}
	return friends, nil
}

// ListPendingFriendRequests 返回等待用户处理的好友申请及申请人资料。
func (u *userService) ListPendingFriendRequests(ctx context.Context, userID uint) ([]dto.PendingFriendRequestOutput, error) {
	all, err := repo.ListFriendRelationsByUserID(ctx, userID, model.FriendRelationStatusPending)
	if err != nil {
		return nil, dbOperationError(err)
	}
	if len(all) == 0 {
		return nil, nil
	}

	ids := make([]uint, 0, len(all))
	idToRelation := make(map[uint]model.FriendRelation, len(all))
	for _, r := range all {
		if r.FriendID == userID {
			ids = append(ids, r.UserID)
			idToRelation[r.UserID] = r
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	users, err := getUserProfilesByIDs(ctx, ids)
	if err != nil {
		return nil, dbOperationError(err)
	}
	userMap := make(map[uint]model.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}

	requests := make([]dto.PendingFriendRequestOutput, 0, len(ids))
	for _, uid := range ids {
		user, ok := userMap[uid]
		if !ok {
			continue
		}
		r := idToRelation[uid]
		requests = append(requests, toPendingFriendRequestOutput(r, user))
	}
	return requests, nil
}

// relationPeerID 返回一条好友关系中相对于当前用户的另一方 ID。
func relationPeerID(relation model.FriendRelation, userID uint) uint {
	if relation.UserID == userID {
		return relation.FriendID
	}
	return relation.UserID
}

// toPendingFriendRequestOutput 将好友关系和申请人资料组合为待处理申请输出。
func toPendingFriendRequestOutput(relation model.FriendRelation, requester model.User) dto.PendingFriendRequestOutput {
	return dto.PendingFriendRequestOutput{
		RequestID:      relation.ID,
		UserID:         requester.ID,
		RequesterEmail: requester.Email,
		Nickname:       requester.Nickname,
		Avatar:         requester.Avatar,
		Status:         relation.Status,
		CreatedAt:      formatMessageTime(relation.CreatedAt),
	}
}
