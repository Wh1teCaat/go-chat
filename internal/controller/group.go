package controller

import (
	"context"

	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/pkg/logger"
	"chat_proj/pkg/response"

	"github.com/gin-gonic/gin"
)

func CreateGroup(c *gin.Context) {
	var input dto.CreateGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.GroupService.CreateGroup(c.Request.Context(), input.Name, userID(c)); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "group created")
}

func UpdateGroupInfo(c *gin.Context) {
	var input dto.UpdateGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.GroupService.UpdateGroupInfo(c.Request.Context(), input.GroupID, userID(c), input); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "group updated")
}

func TransferGroupOwner(c *gin.Context) {
	var input dto.TransferGroupOwnerInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.GroupService.TransferGroupOwner(c.Request.Context(), input.GroupID, userID(c), input.ToUserID); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "ownership transferred")
}

func ListMyGroups(c *gin.Context) {
	groups, err := service.GroupService.ListMyGroups(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, groups)
}

func ListJoinedGroups(c *gin.Context) {
	groups, err := service.GroupService.ListMyJoinedGroups(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, groups)
}

func RequestJoinGroup(c *gin.Context) {
	var input dto.JoinGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	request, err := service.GroupService.RequestJoinGroup(c.Request.Context(), input.GroupID, userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	if request != nil {
		notifyGroupJoinRequest(c.Request.Context(), request)
	}
	response.Message(c, "join request sent")
}

func notifyGroupJoinRequest(ctx context.Context, request *dto.GroupJoinRequestOutput) {
	approverIDs, err := service.GroupService.ListGroupApproverIDs(ctx, request.GroupID)
	if err != nil {
		logger.Warn("notifyGroupJoinRequest failed to list approvers",
			logger.Uint("group_id", request.GroupID),
			logger.String("error", err.Error()))
		return
	}
	pushToUsers(ctx, approverIDs, wsEnvelope{
		Type: dto.WSMessageTypeGroupJoinRequest,
		Data: request,
	})
}

func ReviewGroupJoinRequest(c *gin.Context) {
	var input dto.GroupJoinRequestReviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	input.ReviewerID = userID(c)
	if err := service.GroupService.ReviewGroupJoinRequest(c.Request.Context(), input.RequestID, input); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "request reviewed")
}

func ListGroupJoinRequests(c *gin.Context) {
	var input dto.JoinGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	requests, err := service.GroupService.ListGroupJoinRequests(c.Request.Context(), input.GroupID, userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, requests)
}

func ListMyGroupJoinRequests(c *gin.Context) {
	requests, err := service.GroupService.ListMyGroupJoinRequests(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, requests)
}

func ListReviewableGroupJoinRequests(c *gin.Context) {
	requests, err := service.GroupService.ListReviewableGroupJoinRequests(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, requests)
}

func InviteToGroup(c *gin.Context) {
	var input dto.GroupMemberActionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.GroupService.InviteToGroup(c.Request.Context(), input.GroupID, input.UserID, userID(c)); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "user invited")
}

func LeaveGroup(c *gin.Context) {
	var input dto.JoinGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.GroupService.LeaveGroup(c.Request.Context(), input.GroupID, userID(c)); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "left group")
}

func RemoveGroupMember(c *gin.Context) {
	var input dto.GroupMemberActionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.GroupService.RemoveGroupMember(c.Request.Context(), input.GroupID, input.UserID, userID(c)); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "member removed")
}

func UpdateGroupMemberRole(c *gin.Context) {
	var input dto.UpdateGroupMemberRoleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	if err := service.GroupService.UpdateGroupMemberRole(c.Request.Context(), input.GroupID, input.UserID, userID(c), input); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "member role updated")
}

func ListGroupMembers(c *gin.Context) {
	var input dto.JoinGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}
	members, err := service.GroupService.ListGroupMembers(c.Request.Context(), input.GroupID, userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, members)
}
