package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"chat_proj/internal/dto"
	"chat_proj/internal/messagequeue"
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

var queuedMessages messagequeue.Queue

func InitMessageQueue(queue messagequeue.Queue) {
	queuedMessages = queue
}

// wsBus 是跨实例消息总线。默认进程内直投（单实例/测试）；
// 多实例部署时 main 会注入 RedisBus，推送经 Redis Pub/Sub 广播到所有实例。
var wsBus wsbus.Bus = wsbus.NewLocalBus(orderedDelivery)

func InitWSBus(bus wsbus.Bus) {
	if bus != nil {
		wsBus = bus
	}
}

// pushToUsers 把 envelope 推给目标用户的所有在线连接（可能分布在多个实例）。
// 总线故障不影响主流程：消息已落库，离线端靠重连补拉兜底。
func pushToUsers(ctx context.Context, userIDs []uint, envelope wsEnvelope) {
	if err := publishEnvelope(ctx, userIDs, envelope); err != nil {
		logger.Warn("WSPushPublishFailed",
			logger.String("type", string(envelope.Type)),
			logger.String("error", err.Error()))
	}
}

// publishEnvelope 将本地结构先规范化为 JSON，再交给本地/Redis 总线；两种总线因而走同一
// 有序投递入口。调用方需要知道发布是否成功时可直接使用它，而不是忽略 Redis 降级错误。
func publishEnvelope(ctx context.Context, userIDs []uint, envelope wsEnvelope) error {
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return wsBus.Publish(ctx, userIDs, json.RawMessage(raw))
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

// ConnectWS 将已认证的 HTTP 请求升级为 WebSocket 连接。
func ConnectWS(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	client := ws.NewClient(userID(c), conn, WSHub, handleWSMessage)
	client.Start(c.Request.Context())
}

// handleWSMessage 解析客户端消息、持久化业务数据并推送给相关用户。
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
	if queuedMessages != nil {
		conversationID, err := service.MessageService.ResolveConversationIDForMessage(ctx, senderID, input)
		if err != nil {
			sendWSError(senderID, input.ClientMsgID, err)
			return err
		}
		if err := queuedMessages.Enqueue(ctx, messagequeue.MessageCommand{
			ConversationID: conversationID,
			SenderID:       senderID,
			Input:          input,
		}); err != nil {
			sendWSError(senderID, input.ClientMsgID, err)
			return err
		}
		WSHub.SendTo(senderID, wsEnvelope{
			Type: dto.WSMessageTypeMessageAck,
			Data: dto.MessageAckOutput{ClientMsgID: input.ClientMsgID, Status: "accepted"},
		})
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
			Seq:         result.Message.Seq,
			CreatedAt:   result.Message.CreatedAt,
			Status:      "stored",
		},
	})
	if result.Duplicate {
		// 首次发送可能在消息已提交后进程崩溃，重复请求只补 ACK，不重新广播；
		// 唤醒恢复任务即可补发未确认水位。
		orderedMessagePublisher.Enqueue(result.ConversationID)
		return nil
	}

	event := orderedEventFromResult(senderID, result)
	publishedThrough, publishErr := publishOrderedEvent(ctx, event)
	if publishErr != nil {
		logger.Warn("WSOrderedPublishDeferred",
			logger.Uint("conversation_id", result.ConversationID),
			logger.Any("seq", result.Message.Seq),
			logger.String("error", publishErr.Error()))
		orderedMessagePublisher.Enqueue(result.ConversationID)
		return nil
	}
	if publishedThrough > result.PublishBaseSeq {
		// Redis 已确认连续放行，水位异步批量落库；不会把每条消息的 ACK/推送延迟
		// 再绑定到一条 PostgreSQL UPDATE。崩溃窗口由后台恢复和 seq 去重覆盖。
		orderedPublishedWatermarks.Confirm(result.ConversationID, publishedThrough)
	}
	return nil
}

// HandleQueuedMessages is called once for a Kafka partition micro-batch. The
// database commits the batch before any event is published, and Kafka offsets
// advance only after every resulting message has reached the bus.
func HandleQueuedMessages(ctx context.Context, commands []messagequeue.MessageCommand) error {
	queued := make([]service.QueuedConversationMessage, len(commands))
	for i, command := range commands {
		queued[i] = service.QueuedConversationMessage{
			ConversationID: command.ConversationID,
			SenderID:       command.SenderID,
			Input:          command.Input,
		}
	}
	stored, err := service.MessageService.StoreQueuedConversationMessages(ctx, queued)
	if err != nil {
		return err
	}
	deliveries := make([]wsbus.Delivery, 0, len(stored))
	for i, item := range stored {
		command := commands[i]
		if item.Err != nil {
			if apperrors.HTTPCode(item.Err) >= http.StatusInternalServerError {
				return item.Err
			}
			if err := publishQueuedError(ctx, command.SenderID, command.Input.ClientMsgID, item.Err); err != nil {
				return err
			}
			continue
		}

		result := item.Message
		event := orderedEventFromResult(command.SenderID, result)
		if result.Duplicate {
			events, loadErr := service.MessageService.LoadOrderedMessageEvents(ctx, result.ConversationID, result.Message.Seq-1, 1)
			if loadErr != nil {
				return loadErr
			}
			if len(events) != 1 || events[0].Seq != result.Message.Seq {
				return errors.New("stored kafka message could not be reloaded")
			}
			event = events[0]
		}

		// Kafka has serialized this conversation. The ordinary bus is sufficient;
		// the Redis Lua sequence gate and publication watermarks are bypassed.
		raw, err := json.Marshal(wsEnvelope{Type: dto.WSMessageTypeMessage, Data: event})
		if err != nil {
			return err
		}
		deliveries = append(deliveries, wsbus.Delivery{UserIDs: event.RecipientIDs, Payload: json.RawMessage(raw)})
	}
	if batchBus, ok := wsBus.(wsbus.BatchBus); ok {
		return batchBus.PublishBatch(ctx, deliveries)
	}
	for _, delivery := range deliveries {
		if err := wsBus.Publish(ctx, delivery.UserIDs, delivery.Payload); err != nil {
			return err
		}
	}
	return nil
}

func publishQueuedError(ctx context.Context, senderID uint, clientMsgID string, err error) error {
	return publishEnvelope(ctx, []uint{senderID}, wsEnvelope{
		Type: dto.WSMessageTypeError,
		Data: wsErrorData(err, clientMsgID),
	})
}

// orderedEventFromResult 在消息提交后就能直接构造总线事件，避免热路径为每条消息重新
// 查询会话、成员和消息。ReceiverIDs 来自同次权限校验，发送者也加入接收集合以同步其余设备。
func orderedEventFromResult(senderID uint, result *service.ConversationMessageResult) dto.OrderedMessageEvent {
	recipients := make([]uint, 0, len(result.ReceiverIDs)+1)
	seen := make(map[uint]struct{}, len(result.ReceiverIDs)+1)
	for _, userID := range append([]uint{senderID}, result.ReceiverIDs...) {
		if userID == 0 {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		recipients = append(recipients, userID)
	}
	return dto.OrderedMessageEvent{
		ConversationID: result.ConversationID,
		Seq:            result.Message.Seq,
		PublishBaseSeq: result.PublishBaseSeq,
		SenderID:       senderID,
		TargetType:     result.TargetType,
		TargetID:       result.TargetID,
		RecipientIDs:   recipients,
		Message:        result.Message,
	}
}

// sendWSError 向指定用户推送与客户端消息关联的错误事件。
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

// wsErrorData 将领域错误转换为 WebSocket 错误响应数据。
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
