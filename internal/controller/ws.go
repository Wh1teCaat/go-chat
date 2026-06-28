package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/internal/ws"
	"chat_proj/pkg/apperrors"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

var WSHub = ws.NewHub()

var wsAllowedOrigins = map[string]struct{}{}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		_, ok := wsAllowedOrigins[origin]
		return ok
	},
}

// SetWSAllowedOrigins 从 CORS 配置复用允许的来源，在路由初始化时调用一次。
func SetWSAllowedOrigins(origins []string) {
	m := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			m[o] = struct{}{}
		}
	}
	wsAllowedOrigins = m
}

// InitPresenceStore 把在线状态存储注入 websocket hub。
// 连接建立、断开和心跳刷新都会通过这个 store 写入在线状态。
func InitPresenceStore(store ws.PresenceStore) {
	WSHub.SetPresenceStore(store)
}

type wsEnvelope struct {
	Type dto.WSMessageType `json:"type"`
	Data any               `json:"data,omitempty"`
}

func ConnectWS(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	client := ws.NewClient(userID(c), conn, WSHub, handleWSMessage)
	client.Start(c.Request.Context())
}

func handleWSMessage(ctx context.Context, senderID uint, payload []byte) error {
	// Client 的 readLoop 会把原始 websocket 消息交到这里；当前约定客户端发送 JSON 文本帧。
	var input dto.SendMessageInput
	if err := json.Unmarshal(payload, &input); err != nil {
		sendWSError(senderID, "", err)
		return err
	}
	if input.Type != dto.WSMessageTypeMessage {
		sendWSError(senderID, input.ClientMsgID, apperrors.ErrInvalidInput)
		return nil
	}

	result, err := service.MessageService.SendConversationMessage(ctx, senderID, input)
	if err != nil {
		sendWSError(senderID, input.ClientMsgID, err)
		return err
	}

	// ACK 只发给发送者，用来确认消息已经落库。完整消息会单独推给接收者，ACK 只需要服务端消息 ID。
	WSHub.SendTo(senderID, wsEnvelope{
		Type: dto.WSMessageTypeMessageAck,
		Data: dto.MessageAckOutput{
			ClientMsgID: result.ClientMsgID,
			MessageID:   result.Message.ID,
			CreatedAt:   result.Message.CreatedAt,
		},
	})
	// 接收方不需要主动拉取；服务端推送到达后，浏览器端 onmessage 回调会被触发。
	WSHub.SendToMany(result.ReceiverIDs, wsEnvelope{
		Type: dto.WSMessageTypeMessage,
		Data: result.Message,
	})
	return nil
}

func sendWSError(userID uint, clientMsgID string, err error) {
	status := apperrors.HTTPCode(err)
	fields := []zap.Field{
		logger.Uint("user_id", userID),
		logger.String("code", apperrors.Code(err)),
		logger.Any("status", status),
		logger.String("error", err.Error()),
	}
	if cause := apperrors.Cause(err); cause != nil {
		fields = append(fields, logger.String("cause", cause.Error()))
	}
	if status >= http.StatusInternalServerError {
		logger.Error("WSBusinessError", fields...)
	} else {
		logger.Warn("WSBusinessError", fields...)
	}

	// websocket 升级后不能再用 HTTP 状态码表达错误，所以把 status/code/message 放进错误 envelope。
	WSHub.SendTo(userID, wsEnvelope{Type: dto.WSMessageTypeError, Data: wsErrorData(err, clientMsgID)})
}

func wsErrorData(err error, clientMsgID string) gin.H {
	data := gin.H{
		"status":  apperrors.HTTPCode(err),
		"code":    apperrors.Code(err),
		"message": err.Error(),
	}
	if clientMsgID != "" {
		data["clientMsgID"] = clientMsgID
	}
	return data
}
