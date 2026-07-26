package controller

import (
	"context"
	"net/http"

	"chat_proj/internal/dto"
	"chat_proj/internal/rpc/chatpb"
	"chat_proj/internal/service"
	"chat_proj/pkg/apperrors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// chatGRPCServer 是 chat-logic 服务对 gateway 暴露的 RPC 面。
// 复用与单体 WS handler 完全相同的 service 调用和推送编排，仅传输层不同。
type chatGRPCServer struct {
	chatpb.UnimplementedChatServiceServer
}

func NewChatGRPCServer() chatpb.ChatServiceServer {
	return &chatGRPCServer{}
}

func (s *chatGRPCServer) SendMessage(ctx context.Context, req *chatpb.SendMessageRequest) (*chatpb.SendMessageResponse, error) {
	senderID := uint(req.GetSenderId())
	input := dto.SendMessageInput{
		Type:        dto.WSMessageTypeMessage,
		ClientMsgID: req.GetClientMsgId(),
		TargetType:  dto.MessageTargetType(req.GetTargetType()),
		TargetID:    uint(req.GetTargetId()),
		Content:     req.GetContent(),
	}

	result, err := service.MessageService.SendConversationMessage(ctx, senderID, input)
	if err != nil {
		return nil, grpcStatusError(err)
	}

	// 消息推送（接收方 + 发送者其他设备）由 logic 经总线广播；
	// gateway 只负责用返回值给发起连接回 ACK。
	pushConversationMessage(ctx, senderID, result)

	return &chatpb.SendMessageResponse{
		MessageId:   uint64(result.Message.ID),
		CreatedAt:   result.Message.CreatedAt,
		ClientMsgId: result.ClientMsgID,
		Duplicate:   result.Duplicate,
	}, nil
}

// grpcStatusError 把业务错误映射成 gRPC status，HTTP 语义与 apperrors.HTTPCode 保持一致。
func grpcStatusError(err error) error {
	var code codes.Code
	switch apperrors.HTTPCode(err) {
	case http.StatusBadRequest:
		code = codes.InvalidArgument
	case http.StatusUnauthorized:
		code = codes.Unauthenticated
	case http.StatusForbidden:
		code = codes.PermissionDenied
	case http.StatusNotFound:
		code = codes.NotFound
	case http.StatusConflict:
		code = codes.AlreadyExists
	default:
		code = codes.Internal
	}
	return status.Error(code, err.Error())
}
