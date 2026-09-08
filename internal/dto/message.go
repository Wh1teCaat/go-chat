package dto

type WSMessageType string
type MessageTargetType string

const (
	WSMessageTypeMessage          WSMessageType = "message"
	WSMessageTypeError            WSMessageType = "error"
	WSMessageTypeMessageAck       WSMessageType = "message_ack"
	WSMessageTypeMessageRead      WSMessageType = "message_read"
	WSMessageTypeFriendRequest    WSMessageType = "friend_request"
	WSMessageTypeGroupJoinRequest WSMessageType = "group_join_request"

	MessageTargetTypePrivate MessageTargetType = "private"
	MessageTargetTypeGroup   MessageTargetType = "group"
)

type SendMessageInput struct {
	Type WSMessageType `json:"type" binding:"required"`
	// ClientMsgID 由客户端生成，用来把本地“发送中”的消息和服务端 ACK 对上。
	ClientMsgID string `json:"clientMsgID"`
	// TargetType/TargetID 同时覆盖单聊和群聊，避免把 conversationID 暴露给客户端。
	TargetType MessageTargetType `json:"targetType" binding:"required"`
	TargetID   uint              `json:"targetID" binding:"required"`
	Content    string            `json:"content" binding:"required"`
}

type MessageAckOutput struct {
	// ClientMsgID 原样返回客户端；MessageID 是服务端落库后的真实消息 ID。
	ClientMsgID string `json:"clientMsgID,omitempty"`
	MessageID   uint   `json:"messageID"`
	CreatedAt   string `json:"createdAt"`
}

type MessageReadOutput struct {
	TargetType MessageTargetType `json:"targetType"`
	TargetID   uint              `json:"targetID"`
	MessageID  uint              `json:"messageID"`
	ReaderID   uint              `json:"readerID"`
}

type ListMessagesInput struct {
	TargetType MessageTargetType `json:"targetType" binding:"required"`
	TargetID   uint              `json:"targetID" binding:"required"`
	// BeforeMessageID 是向上翻历史的游标；为空时读取最新一页。
	BeforeMessageID uint `json:"beforeMessageID"`
	// AfterMessageID 是断线重连后的增量补拉游标，按 id 升序返回更新的消息；与 BeforeMessageID 互斥。
	AfterMessageID uint `json:"afterMessageID"`
	Limit          int  `json:"limit"`
}

type MarkMessageReadInput struct {
	TargetType MessageTargetType `json:"targetType" binding:"required"`
	TargetID   uint              `json:"targetID" binding:"required"`
	MessageID  uint              `json:"messageID" binding:"required"`
}

type MessageSessionOutput struct {
	TargetType  MessageTargetType      `json:"targetType"`
	TargetID    uint                   `json:"targetID"`
	Name        string                 `json:"name"`
	Avatar      string                 `json:"avatar"`
	LastMessage *MessageSnapshotOutput `json:"lastMessage,omitempty"`
	UnreadCount int64                  `json:"unreadCount"`
	UpdatedAt   string                 `json:"updatedAt"`
}

type MessageSnapshotOutput struct {
	ID        uint   `json:"id"`
	SenderID  uint   `json:"senderID"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

type MessageOutput struct {
	ID        uint   `json:"id"`
	SenderID  uint   `json:"senderID"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
	// TargetType/TargetID 只在 WS 推送时填充，标明消息属于接收端视角的哪个会话，
	// 客户端据此把消息归档到正确的会话而不是一律插入当前窗口。
	TargetType MessageTargetType `json:"targetType,omitempty"`
	TargetID   uint              `json:"targetID,omitempty"`
	// ClientMsgID 只在推送给发送方自己的连接时填充，发送端据此和本地"发送中"的消息去重。
	ClientMsgID string `json:"clientMsgID,omitempty"`
}
