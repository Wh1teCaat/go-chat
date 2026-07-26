// Package gateway 是拆分部署后的 WebSocket 接入层：
// 只负责连接生命周期、JWT 认证、ACK/错误回写和总线消息投递；
// 消息的校验、落库、推送编排全部通过 gRPC 委托给 chat-logic 服务。
// gateway 无业务状态，可按连接数水平扩容。
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"chat_proj/internal/dto"
	"chat_proj/internal/rpc/chatpb"
	"chat_proj/internal/ws"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	hub      *ws.Hub
	client   chatpb.ChatServiceClient
	upgrader websocket.Upgrader
}

func New(hub *ws.Hub, client chatpb.ChatServiceClient, allowedOrigins []string) *Server {
	originSet := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin = strings.TrimSpace(origin); origin != "" {
			originSet[origin] = struct{}{}
		}
	}
	return &Server{
		hub:    hub,
		client: client,
		upgrader: websocket.Upgrader{
			// 与单体版约定一致：子协议携带 "chat" 和 "bearer.<token>"，服务端固定选 "chat"。
			Subprotocols: []string{"chat"},
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				_, ok := originSet[origin]
				return ok
			},
		},
	}
}

func (g *Server) Hub() *ws.Hub {
	return g.hub
}

func (g *Server) ConnectWS(c *gin.Context) {
	conn, err := g.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := ws.NewClient(c.GetUint("user_id"), conn, g.hub, g.handleIncoming)
	client.Start(c.Request.Context())
}

type envelope struct {
	Type dto.WSMessageType `json:"type"`
	Data any               `json:"data,omitempty"`
}

func (g *Server) handleIncoming(ctx context.Context, senderID uint, payload []byte) error {
	var input dto.SendMessageInput
	if err := json.Unmarshal(payload, &input); err != nil {
		g.sendError(senderID, "", http.StatusBadRequest, "invalid_input", "invalid message payload")
		return err
	}
	if input.Type != dto.WSMessageTypeMessage {
		g.sendError(senderID, input.ClientMsgID, http.StatusBadRequest, "invalid_input", "invalid message type")
		return nil
	}

	resp, err := g.client.SendMessage(ctx, &chatpb.SendMessageRequest{
		SenderId:    uint64(senderID),
		ClientMsgId: input.ClientMsgID,
		TargetType:  string(input.TargetType),
		TargetId:    uint64(input.TargetID),
		Content:     input.Content,
	})
	if err != nil {
		httpStatus, code, message := grpcErrorToWS(err)
		logger.Warn("GatewaySendMessageFailed",
			logger.Uint("user_id", senderID),
			logger.String("code", code),
			logger.String("error", message))
		g.sendError(senderID, input.ClientMsgID, httpStatus, code, message)
		return err
	}

	// ACK 回给发起连接所在实例（就是本实例）的该用户全部本地连接；
	// 消息本体由 logic 经总线广播，gateway 的订阅端负责投递。
	g.hub.SendTo(senderID, envelope{
		Type: dto.WSMessageTypeMessageAck,
		Data: dto.MessageAckOutput{
			ClientMsgID: resp.GetClientMsgId(),
			MessageID:   uint(resp.GetMessageId()),
			CreatedAt:   resp.GetCreatedAt(),
		},
	})
	return nil
}

func (g *Server) sendError(userID uint, clientMsgID string, httpStatus int, code, message string) {
	data := gin.H{"status": httpStatus, "code": code, "message": message}
	if clientMsgID != "" {
		data["clientMsgID"] = clientMsgID
	}
	g.hub.SendTo(userID, envelope{Type: dto.WSMessageTypeError, Data: data})
}

// grpcErrorToWS 把 logic 返回的 gRPC status 还原成 WS 错误 envelope 需要的字段，
// 状态码映射与 logic 侧 grpcStatusError 对称。
func grpcErrorToWS(err error) (httpStatus int, code, message string) {
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusInternalServerError, "internal_error", "internal error"
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return http.StatusBadRequest, "invalid_input", st.Message()
	case codes.Unauthenticated:
		return http.StatusUnauthorized, "invalid_token", st.Message()
	case codes.PermissionDenied:
		return http.StatusForbidden, "permission_denied", st.Message()
	case codes.NotFound:
		return http.StatusNotFound, "not_found", st.Message()
	case codes.AlreadyExists:
		return http.StatusConflict, "conflict", st.Message()
	case codes.Unavailable, codes.DeadlineExceeded:
		// logic 不可用对客户端来说是可重试的临时故障，前端会把消息标记为发送失败。
		return http.StatusServiceUnavailable, "logic_unavailable", "chat service temporarily unavailable"
	default:
		return http.StatusInternalServerError, "internal_error", st.Message()
	}
}
