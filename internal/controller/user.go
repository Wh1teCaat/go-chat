package controller

import (
	"chat_proj/internal/auth"
	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/pkg/apperrors"
	"chat_proj/pkg/response"
	"errors"

	"github.com/gin-gonic/gin"
)

func Register(c *gin.Context) {
	var input dto.RegisterUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.UserService.Register(c.Request.Context(), input); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "registration successful")
}

func Login(c *gin.Context) {
	var input dto.LoginUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	user, err := service.UserService.Login(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}

	token, expireAt, err := auth.GenerateAccessToken(user.UserID, user.Email)
	if err != nil {
		response.Error(c, apperrors.WithCause(errors.New("internal error"), "failed to generate token", err))
		return
	}
	refreshToken, refreshExpireAt, err := auth.GenerateRefreshToken(user.UserID, user.Email)
	if err != nil {
		response.Error(c, apperrors.WithCause(errors.New("internal error"), "failed to generate refresh token", err))
		return
	}

	response.OK(c, tokenResponse(token, expireAt, refreshToken, refreshExpireAt))
}

func RefreshToken(c *gin.Context) {
	var input dto.RefreshTokenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}

	claims, err := auth.ValidateRefreshToken(input.RefreshToken)
	if err != nil {
		response.Error(c, apperrors.WithCause(apperrors.ErrInvalidToken, "invalid refresh token", err))
		return
	}

	token, expireAt, err := auth.GenerateAccessToken(claims.UserID, claims.Username)
	if err != nil {
		response.Error(c, apperrors.WithCause(errors.New("internal error"), "failed to generate token", err))
		return
	}
	refreshToken, refreshExpireAt, err := auth.GenerateRefreshToken(claims.UserID, claims.Username)
	if err != nil {
		response.Error(c, apperrors.WithCause(errors.New("internal error"), "failed to generate refresh token", err))
		return
	}

	response.OK(c, tokenResponse(token, expireAt, refreshToken, refreshExpireAt))
}

func tokenResponse(token string, expireAt int64, refreshToken string, refreshExpireAt int64) gin.H {
	return gin.H{
		"token":             token,
		"expire_at":         expireAt,
		"refresh_token":     refreshToken,
		"refresh_expire_at": refreshExpireAt,
	}
}

func UpdateUserInfo(c *gin.Context) {
	var input dto.UpdateUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.UserService.UpdateUserInfo(c.Request.Context(), userID(c), input); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "update successful")
}

func AddFriend(c *gin.Context) {
	var input dto.AddFriendInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	result, err := service.UserService.AddFriendByEmail(c.Request.Context(), userID(c), input.FriendEmail)
	if err != nil {
		response.Error(c, err)
		return
	}
	if result != nil {
		WSHub.SendTo(result.ReceiverID, wsEnvelope{
			Type: dto.WSMessageTypeFriendRequest,
			Data: result.Request,
		})
	}
	response.Message(c, "friend request sent")
}

func AcceptFriend(c *gin.Context) {
	var input dto.FriendRequestActionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.UserService.AcceptFriend(c.Request.Context(), userID(c), input.RequestID); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "friend request accepted")
}

func RejectFriend(c *gin.Context) {
	var input dto.FriendRequestActionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.UserService.RejectFriend(c.Request.Context(), userID(c), input.RequestID); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "friend request rejected")
}

func RemoveFriend(c *gin.Context) {
	var input dto.FriendTargetInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.UserService.RemoveFriend(c.Request.Context(), userID(c), input.FriendID); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "friend removed")
}

func ListFriends(c *gin.Context) {
	friends, err := service.UserService.ListFriends(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, friends)
}

func ListPendingFriendRequests(c *gin.Context) {
	requests, err := service.UserService.ListPendingFriendRequests(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, requests)
}

func userID(c *gin.Context) uint {
	return c.GetUint("user_id")
}
