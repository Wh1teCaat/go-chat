package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/internal/ws"
	"chat_proj/internal/wsbus"
	"chat_proj/pkg/apperrors"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

var WSHub = ws.NewHub()

// wsBus 是跨实例消息总线。默认进程内直投（单实例/测试）；
// 多实例部署时 main 会注入 RedisBus，推送经 Redis Pub/Sub 广播到所有实例。
var wsBus wsbus.Bus = wsbus.NewLocalBus(WSHub)

func InitWSBus(bus wsbus.Bus) {
	if bus != nil {
		wsBus = bus
	}
}

// pushToUsers 把 envelope 推给目标用户的所有在线连接（可能分布在多个实例）。
// 总线故障不影响主流程：消息已落库，离线端靠重连补拉兜底。
func pushToUsers(ctx context.Context, userIDs []uint, envelope wsEnvelope) {
	if err := wsBus.Publish(ctx, userIDs, envelope); err != nil {
		logger.Warn("WSPushPublishFailed",
			logger.String("type", string(envelope.Type)),
			logger.String("error", err.Error()))
	}
}

var wsAllowedOrigins = map[string]struct{}{}

var wsUpgrader = websocket.Upgrader{
	// 客户端在子协议里同时携带 "chat" 和 "bearer.<token>"；服务端固定选择 "chat" 回应，
	// token 条目只用于认证（见 middleware.AuthRequired），不作为协商结果。
	Subprotocols: []string{"chat"},
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

	// ACK 只对发起发送的那个连接有意义（靠 clientMsgID 对上本地"发送中"的消息），
	// 而发起连接必然在本实例上，所以 ACK 走本地 Hub 直投，不经过总线；
	// 发送者其他实例上的设备会通过下面的消息推送拿到完整消息。
	WSHub.SendTo(senderID, wsEnvelope{
		Type: dto.WSMessageTypeMessageAck,
		Data: dto.MessageAckOutput{
			ClientMsgID: result.ClientMsgID,
			MessageID:   result.Message.ID,
			CreatedAt:   result.Message.CreatedAt,
		},
	})
	pushConversationMessage(ctx, senderID, result)
	return nil
}

// pushConversationMessage 把落库后的消息经总线推给接收方和发送者的其他设备。
// 单体的 WS handler 和拆分部署后的 gRPC handler（logic 服务）共用这段推送编排。
func pushConversationMessage(ctx context.Context, senderID uint, result *service.ConversationMessageResult) {
	// 重复发送（ACK 丢失后的客户端重试）只需重发 ACK；消息第一次发送时已经推给过接收方。
	if result.Duplicate {
		return
	}

	// 推给接收方时带上接收端视角的会话目标：群聊就是群 ID；
	// 私聊时接收方看到的目标是发送者本人，客户端据此归档到正确会话。
	receiverMessage := result.Message
	receiverMessage.TargetType = result.TargetType
	receiverMessage.TargetID = result.TargetID
	if result.TargetType == dto.MessageTargetTypePrivate {
		receiverMessage.TargetID = senderID
	}
	// 接收方不需要主动拉取；服务端推送到达后，浏览器端 onmessage 回调会被触发。
	pushToUsers(ctx, result.ReceiverIDs, wsEnvelope{
		Type: dto.WSMessageTypeMessage,
		Data: receiverMessage,
	})

	// 消息本体也推给发送者的全部连接（多标签页/多设备），带 clientMsgID 供发送端本地去重。
	// 发起消息的那个连接会同时收到 ACK 和这条推送，客户端按消息 ID 去重。
	senderMessage := result.Message
	senderMessage.TargetType = result.TargetType
	senderMessage.TargetID = result.TargetID
	senderMessage.ClientMsgID = result.ClientMsgID
	pushToUsers(ctx, []uint{senderID}, wsEnvelope{
		Type: dto.WSMessageTypeMessage,
		Data: senderMessage,
	})
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
	// 错误和 ACK 一样只对发起连接有意义，走本地 Hub，不经过总线。
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
