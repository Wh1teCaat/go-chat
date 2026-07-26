package controller

import (
	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/pkg/response"

	"github.com/gin-gonic/gin"
)

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

func ListMessageSessions(c *gin.Context) {
	sessions, err := service.MessageService.ListSessions(c.Request.Context(), userID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, sessions)
}
