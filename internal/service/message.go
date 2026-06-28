package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"chat_proj/pkg/apperrors"

	"gorm.io/gorm"
)

type messageService struct{}

var MessageService = new(messageService)

type ConversationMessageResult struct {
	Message     dto.MessageOutput
	ReceiverIDs []uint
	// ClientMsgID 只用于 ACK 关联客户端本地消息，不参与服务端消息存储。
	ClientMsgID string
}

type MessageReadResult struct {
	Event       dto.MessageReadOutput
	ReceiverIDs []uint
}

func (s *messageService) SendConversationMessage(ctx context.Context, senderID uint, input dto.SendMessageInput) (*ConversationMessageResult, error) {
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "message content is required")
	}

	conversation, err := s.resolveConversation(ctx, senderID, input.TargetType, input.TargetID)
	if err != nil {
		return nil, err
	}

	fileID, hasFile := fileIDFromMessageContent(content)
	if hasFile {
		file, err := repo.GetFileByID(ctx, fileID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, apperrors.WithMessage(apperrors.ErrNotFound, "file not found")
			}
			return nil, dbOperationError(err)
		}
		if file.UserID != senderID {
			return nil, apperrors.ErrPermissionDenied
		}
	}

	message := &model.Message{
		ConversationID: conversation.ID,
		SenderID:       senderID,
		Content:        content,
	}
	if err := repo.CreateMessage(ctx, message); err != nil {
		return nil, dbOperationError(err)
	}
	if hasFile {
		// 文件消息落库后，把文件绑定到当前会话，下载接口据此判断会话成员权限。
		if err := repo.BindFileToConversation(ctx, fileID, senderID, conversation.ID); err != nil {
			return nil, dbOperationError(err)
		}
	}

	members, err := repo.ListConversationMembersByConversationID(ctx, conversation.ID)
	if err != nil {
		return nil, dbOperationError(err)
	}

	receiverIDs := make([]uint, 0, len(members))
	for _, member := range members {
		if member.UserID != senderID {
			receiverIDs = append(receiverIDs, member.UserID)
		}
	}

	return &ConversationMessageResult{
		Message:     toMessageOutput(*message),
		ReceiverIDs: receiverIDs,
		ClientMsgID: input.ClientMsgID,
	}, nil
}

type fileMessageContent struct {
	Kind string `json:"kind"`
	ID   uint   `json:"id"`
}

func fileIDFromMessageContent(content string) (uint, bool) {
	var payload fileMessageContent
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return 0, false
	}
	if payload.Kind != "file" || payload.ID == 0 {
		return 0, false
	}
	return payload.ID, true
}

func (s *messageService) ListMessages(ctx context.Context, userID uint, input dto.ListMessagesInput) ([]dto.MessageOutput, error) {
	conversation, err := s.resolveConversation(ctx, userID, input.TargetType, input.TargetID)
	if err != nil {
		return nil, err
	}

	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	messages, err := repo.ListMessagesByConversationID(ctx, conversation.ID, input.BeforeMessageID, limit)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return toMessageOutputs(messages), nil
}

func (s *messageService) MarkMessageRead(ctx context.Context, userID uint, input dto.MarkMessageReadInput) (*MessageReadResult, error) {
	conversation, err := s.resolveConversation(ctx, userID, input.TargetType, input.TargetID)
	if err != nil {
		return nil, err
	}

	message, err := repo.GetMessageByID(ctx, input.MessageID)
	if err != nil {
		return nil, apperrors.WithMessage(apperrors.ErrNotFound, "message not found")
	}
	if message.ConversationID != conversation.ID {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "message does not belong to conversation")
	}

	if err := repo.UpdateConversationMemberLastReadMessageID(ctx, conversation.ID, userID, input.MessageID); err != nil {
		return nil, dbOperationError(err)
	}

	members, err := repo.ListConversationMembersByConversationID(ctx, conversation.ID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	receiverIDs := make([]uint, 0, len(members))
	for _, member := range members {
		if member.UserID != userID {
			receiverIDs = append(receiverIDs, member.UserID)
		}
	}

	eventTargetID := input.TargetID
	if input.TargetType == dto.MessageTargetTypePrivate {
		// 私聊已读事件是发给“读者之外”的成员；对接收方来说，目标用户应该是读者本人。
		eventTargetID = userID
	}

	return &MessageReadResult{
		Event: dto.MessageReadOutput{
			TargetType: input.TargetType,
			TargetID:   eventTargetID,
			MessageID:  input.MessageID,
			ReaderID:   userID,
		},
		ReceiverIDs: receiverIDs,
	}, nil
}

func (s *messageService) ListSessions(ctx context.Context, userID uint) ([]dto.MessageSessionOutput, error) {
	conversations, err := repo.ListConversationsByUserID(ctx, userID)
	if err != nil {
		return nil, dbOperationError(err)
	}

	sessions := make([]dto.MessageSessionOutput, 0, len(conversations))
	for _, conversation := range conversations {
		session, ok, err := s.buildSession(ctx, userID, conversation)
		if err != nil {
			return nil, err
		}
		if ok {
			sessions = append(sessions, session)
		}
	}
	sortMessageSessions(sessions)
	return sessions, nil
}

func (s *messageService) buildSession(ctx context.Context, userID uint, conversation model.Conversation) (dto.MessageSessionOutput, bool, error) {
	members, err := repo.ListConversationMembersByConversationID(ctx, conversation.ID)
	if err != nil {
		return dto.MessageSessionOutput{}, false, dbOperationError(err)
	}

	var lastReadMessageID uint
	var peerUserID uint
	for _, member := range members {
		if member.UserID == userID {
			lastReadMessageID = member.LastReadMessageID
			continue
		}
		peerUserID = member.UserID
	}

	session := dto.MessageSessionOutput{}
	switch conversation.Type {
	case model.ConversationTypePrivate:
		if peerUserID == 0 {
			return dto.MessageSessionOutput{}, false, nil
		}
		peer, err := getUserProfile(ctx, peerUserID)
		if err != nil {
			return dto.MessageSessionOutput{}, false, dbOperationError(err)
		}
		session.TargetType = dto.MessageTargetTypePrivate
		session.TargetID = peer.ID
		session.Name = peer.Nickname
		session.Avatar = peer.Avatar
	case model.ConversationTypeGroup:
		if conversation.GroupID == nil {
			return dto.MessageSessionOutput{}, false, nil
		}
		group, err := getGroupInfo(ctx, *conversation.GroupID)
		if err != nil {
			return dto.MessageSessionOutput{}, false, dbOperationError(err)
		}
		session.TargetType = dto.MessageTargetTypeGroup
		session.TargetID = group.ID
		session.Name = group.Name
	default:
		return dto.MessageSessionOutput{}, false, nil
	}

	lastMessage, err := repo.GetLastMessageByConversationID(ctx, conversation.ID)
	if err == nil {
		session.LastMessage = &dto.MessageSnapshotOutput{
			ID:        lastMessage.ID,
			SenderID:  lastMessage.SenderID,
			Content:   lastMessage.Content,
			CreatedAt: formatMessageTime(lastMessage.CreatedAt),
		}
		session.UpdatedAt = formatMessageTime(lastMessage.CreatedAt)
	} else {
		// 新会话可能没有消息，此时用会话更新时间参与会话列表排序。
		session.UpdatedAt = formatMessageTime(conversation.UpdatedAt)
	}

	unreadCount, err := repo.CountUnreadMessages(ctx, conversation.ID, userID, lastReadMessageID)
	if err != nil {
		return dto.MessageSessionOutput{}, false, dbOperationError(err)
	}
	session.UnreadCount = unreadCount
	return session, true, nil
}

func formatMessageTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatMessageTime(*t)
}

func toMessageOutput(message model.Message) dto.MessageOutput {
	return dto.MessageOutput{
		ID:        message.ID,
		SenderID:  message.SenderID,
		Content:   message.Content,
		CreatedAt: formatMessageTime(message.CreatedAt),
	}
}

func toMessageOutputs(messages []model.Message) []dto.MessageOutput {
	result := make([]dto.MessageOutput, 0, len(messages))
	for _, message := range messages {
		result = append(result, toMessageOutput(message))
	}
	return result
}

func sortMessageSessions(sessions []dto.MessageSessionOutput) {
	sort.SliceStable(sessions, func(i, j int) bool {
		left := sessions[i]
		right := sessions[j]
		// 会话列表按最近活跃时间倒序；同一时间下用最后一条消息 ID 保持顺序稳定。
		if left.UpdatedAt != right.UpdatedAt {
			return left.UpdatedAt > right.UpdatedAt
		}
		return lastMessageID(left) > lastMessageID(right)
	})
}

func lastMessageID(session dto.MessageSessionOutput) uint {
	if session.LastMessage == nil {
		return 0
	}
	return session.LastMessage.ID
}

func (s *messageService) resolveConversation(ctx context.Context, userID uint, targetType dto.MessageTargetType, targetID uint) (*model.Conversation, error) {
	// 客户端只传 targetType/targetID。这里把目标解析成内部 conversation，
	// 并完成成员校验；被删好友或退群后不会再有权限发消息或看历史。
	switch targetType {
	case dto.MessageTargetTypePrivate:
		conversation, err := repo.GetPrivateConversationBetweenUsers(ctx, userID, targetID)
		if err != nil {
			return nil, apperrors.WithMessage(apperrors.ErrNotFound, "private conversation not found")
		}
		return conversation, nil
	case dto.MessageTargetTypeGroup:
		conversation, err := repo.GetConversationByGroupID(ctx, targetID)
		if err != nil {
			return nil, apperrors.WithMessage(apperrors.ErrNotFound, "group conversation not found")
		}

		inConversation, err := repo.IsUserInConversation(ctx, conversation.ID, userID)
		if err != nil {
			return nil, dbOperationError(err)
		}
		if !inConversation {
			return nil, apperrors.ErrPermissionDenied
		}
		return conversation, nil
	default:
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid message target type")
	}
}
