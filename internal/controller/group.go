package controller

import (
	"context"

	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/pkg/logger"
	"chat_proj/pkg/response"

	"github.com/gin-gonic/gin"
)

// CreateGroup 校验请求参数并创建由当前用户拥有的群组。
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

// UpdateGroupInfo 更新群组的名称或描述信息。
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

// TransferGroupOwner 将群主身份转移给指定群成员。
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

// ListMyGroups 返回当前用户创建的群组列表。
func ListMyGroups(c *gin.Context) {
	groups, err := service.GroupService.ListMyGroups(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, groups)
}

// ListJoinedGroups 返回当前用户加入的群组列表。
func ListJoinedGroups(c *gin.Context) {
	groups, err := service.GroupService.ListMyJoinedGroups(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, groups)
}

// RequestJoinGroup 提交当前用户的入群申请并通知审核人。
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

// notifyGroupJoinRequest 向群组审核人推送新的入群申请事件。
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

// ReviewGroupJoinRequest 审批指定的入群申请。
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

// ListGroupJoinRequests 返回指定群组的入群申请列表。
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

// ListMyGroupJoinRequests 返回当前用户提交的入群申请列表。
func ListMyGroupJoinRequests(c *gin.Context) {
	requests, err := service.GroupService.ListMyGroupJoinRequests(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, requests)
}

// ListReviewableGroupJoinRequests 返回当前用户有权审核的入群申请。
func ListReviewableGroupJoinRequests(c *gin.Context) {
	requests, err := service.GroupService.ListReviewableGroupJoinRequests(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, requests)
}

// InviteToGroup 将指定用户邀请并加入群组。
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

// LeaveGroup 让当前用户退出指定群组。
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

// RemoveGroupMember 将指定成员移出群组。
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

// UpdateGroupMemberRole 修改指定群成员的角色。
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

// ListGroupMembers 返回指定群组的成员列表。
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
