package controller

import (
	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/pkg/response"

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

	pair, err := service.TokenService.IssueTokenPair(c.Request.Context(), user.UserID, user.Email)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, tokenResponse(pair))
}

func RefreshToken(c *gin.Context) {
	var input dto.RefreshTokenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}

	// 校验、allowlist 检查和轮换都在 TokenService 内完成；旧 refresh token 从此不可再用。
	pair, err := service.TokenService.RefreshTokenPair(c.Request.Context(), input.RefreshToken)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, tokenResponse(pair))
}

// Logout 吊销 refresh token。access token 本身短期有效、无状态，不做黑名单，
// 过期后没有可用的 refresh token 就等于完全登出。
func Logout(c *gin.Context) {
	var input dto.RefreshTokenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.TokenService.RevokeRefreshToken(c.Request.Context(), input.RefreshToken); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "logged out")
}

func tokenResponse(pair *service.TokenPair) gin.H {
	return gin.H{
		"token":             pair.AccessToken,
		"expire_at":         pair.AccessExpireAt,
		"refresh_token":     pair.RefreshToken,
		"refresh_expire_at": pair.RefreshExpireAt,
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
		pushToUsers(c.Request.Context(), []uint{result.ReceiverID}, wsEnvelope{
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
