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

func relationPeerID(relation model.FriendRelation, userID uint) uint {
	if relation.UserID == userID {
		return relation.FriendID
	}
	return relation.UserID
}

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
