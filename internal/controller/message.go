package controller

import (
	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/pkg/response"

	"github.com/gin-gonic/gin"
)

// ListMessages 分页返回当前用户可访问的会话消息。
func ListMessages(c *gin.Context) {
	var input dto.ListMessagesInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}

	messages, err := service.MessageService.ListMessages(c.Request.Context(), userID(c), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, messages)
}

// MarkMessageRead 更新当前用户在会话中的最后已读消息位置。
func MarkMessageRead(c *gin.Context) {
	var input dto.MarkMessageReadInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}

	result, err := service.MessageService.MarkMessageRead(c.Request.Context(), userID(c), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	// 已读回执要送达可能连在其他实例上的会话成员，走总线。
	pushToUsers(c.Request.Context(), result.ReceiverIDs, wsEnvelope{
		Type: dto.WSMessageTypeMessageRead,
		Data: result.Event,
	})
	response.Message(c, "message read")
}

// ListMessageSessions 返回当前用户的消息会话摘要列表。
func ListMessageSessions(c *gin.Context) {
	sessions, err := service.MessageService.ListSessions(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, sessions)
}
