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
	"chat_proj/internal/repository"
	"chat_proj/pkg/apperrors"

	"gorm.io/gorm"
)

type messageService struct{}

var MessageService = new(messageService)

type ConversationMessageResult struct {
	Message     dto.MessageOutput
	ReceiverIDs []uint
	// ConversationID 是内部会话 ID，推送时作为总线的顺序域键（同会话事件保序）。
	ConversationID uint
	// ClientMsgID 用于 ACK 关联客户端本地消息；非空时也会落库参与幂等去重。
	ClientMsgID string
	// TargetType/TargetID 是发送方视角的会话目标，推送给发送方其他设备时使用。
	TargetType dto.MessageTargetType
	TargetID   uint
	// Duplicate 表示本次是重复发送（clientMsgID 已落库），只需重发 ACK，不能再推给接收方。
	Duplicate bool
}

type MessageReadResult struct {
	Event       dto.MessageReadOutput
	ReceiverIDs []uint
	// ConversationID 作为总线顺序域键：已读事件与同会话的消息推送保持相对顺序。
	ConversationID uint
}

func (s *messageService) SendConversationMessage(ctx context.Context, senderID uint, input dto.SendMessageInput) (*ConversationMessageResult, error) {
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "message content is required")
	}
	clientMsgID := strings.TrimSpace(input.ClientMsgID)
	if len(clientMsgID) > 64 {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "clientMsgID too long")
	}

	conversation, err := s.resolveConversation(ctx, senderID, input.TargetType, input.TargetID)
	if err != nil {
		return nil, err
	}

	// 幂等去重：客户端 ACK 丢失后重发同一条消息时，直接返回已落库的消息，不再二次入库。
	if clientMsgID != "" {
		existing, err := repo.GetMessageBySenderAndClientMsgID(ctx, senderID, clientMsgID)
		if err == nil {
			return s.duplicateMessageResult(existing, clientMsgID, input)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dbOperationError(err)
		}
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
	if clientMsgID != "" {
		message.ClientMsgID = &clientMsgID
	}
	// 消息落库和文件绑定必须同事务：绑定失败时回滚消息，避免留下接收方无权下载附件的孤儿消息。
	err = repo.WithTransaction(func(tx *repository.Repository) error {
		if err := tx.CreateMessage(ctx, message); err != nil {
			return err
		}
		if hasFile {
			// 文件消息落库后，把文件绑定到当前会话，下载接口据此判断会话成员权限。
			return tx.BindFileToConversation(ctx, fileID, senderID, conversation.ID)
		}
		return nil
	})
	if err != nil {
		// 并发重发可能同时通过上面的查重后撞唯一索引；此时重查按重复消息返回。
		if clientMsgID != "" {
			if existing, qErr := repo.GetMessageBySenderAndClientMsgID(ctx, senderID, clientMsgID); qErr == nil {
				return s.duplicateMessageResult(existing, clientMsgID, input)
			}
		}
		return nil, dbOperationError(err)
	}

	receiverIDs, err := s.conversationReceiverIDs(ctx, conversation.ID, senderID)
	if err != nil {
		return nil, err
	}

	return &ConversationMessageResult{
		Message:        toMessageOutput(*message),
		ReceiverIDs:    receiverIDs,
		ConversationID: conversation.ID,
		ClientMsgID:    clientMsgID,
		TargetType:     input.TargetType,
		TargetID:       input.TargetID,
	}, nil
}

func (s *messageService) duplicateMessageResult(message *model.Message, clientMsgID string, input dto.SendMessageInput) (*ConversationMessageResult, error) {
	return &ConversationMessageResult{
		Message:        toMessageOutput(*message),
		ConversationID: message.ConversationID,
		ClientMsgID:    clientMsgID,
		TargetType:     input.TargetType,
		TargetID:       input.TargetID,
		Duplicate:      true,
	}, nil
}

func (s *messageService) conversationReceiverIDs(ctx context.Context, conversationID, senderID uint) ([]uint, error) {
	members, err := repo.ListConversationMembersByConversationID(ctx, conversationID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	receiverIDs := make([]uint, 0, len(members))
	for _, member := range members {
		if member.UserID != senderID {
			receiverIDs = append(receiverIDs, member.UserID)
		}
	}
	return receiverIDs, nil
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
	// afterMessageID 用于断线重连后的增量补拉，与向上翻历史的 beforeMessageID 互斥。
	if input.AfterMessageID > 0 {
		messages, err := repo.ListMessagesAfterMessageID(ctx, conversation.ID, input.AfterMessageID, limit)
		if err != nil {
			return nil, dbOperationError(err)
		}
		return toMessageOutputs(messages), nil
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
		ReceiverIDs:    receiverIDs,
		ConversationID: conversation.ID,
	}, nil
}

// ListSessions 构建会话列表。所有数据都按会话集合批量查询（固定 6 条 SQL），
// 不随会话数量增长发起更多查询（此前是每个会话 4 次查询的 N+1 写法）。
func (s *messageService) ListSessions(ctx context.Context, userID uint) ([]dto.MessageSessionOutput, error) {
	conversations, err := repo.ListConversationsByUserID(ctx, userID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	if len(conversations) == 0 {
		return []dto.MessageSessionOutput{}, nil
	}

	conversationIDs := make([]uint, 0, len(conversations))
	privateIDs := make([]uint, 0, len(conversations))
	groupIDs := make([]uint, 0, len(conversations))
	for _, conversation := range conversations {
		conversationIDs = append(conversationIDs, conversation.ID)
		switch conversation.Type {
		case model.ConversationTypePrivate:
			privateIDs = append(privateIDs, conversation.ID)
		case model.ConversationTypeGroup:
			if conversation.GroupID != nil {
				groupIDs = append(groupIDs, *conversation.GroupID)
			}
		}
	}

	// 私聊会话的成员列表：用于找出对端用户。
	privateMembers, err := repo.ListConversationMembersByConversationIDs(ctx, privateIDs)
	if err != nil {
		return nil, dbOperationError(err)
	}
	peerByConversation := make(map[uint]uint, len(privateIDs))
	peerIDSet := make(map[uint]struct{})
	for _, member := range privateMembers {
		if member.UserID != userID {
			peerByConversation[member.ConversationID] = member.UserID
			peerIDSet[member.UserID] = struct{}{}
		}
	}
	peerIDs := make([]uint, 0, len(peerIDSet))
	for id := range peerIDSet {
		peerIDs = append(peerIDs, id)
	}

	// 走带缓存的批量读取：命中 Redis 的用户资料不再回表。
	peers, err := getUserProfilesByIDs(ctx, peerIDs)
	if err != nil {
		return nil, dbOperationError(err)
	}
	peerByID := make(map[uint]model.User, len(peers))
	for _, peer := range peers {
		peerByID[peer.ID] = peer
	}

	groups, err := repo.GetGroupsByIDs(ctx, groupIDs)
	if err != nil {
		return nil, dbOperationError(err)
	}
	groupByID := make(map[uint]model.Group, len(groups))
	for _, group := range groups {
		groupByID[group.ID] = group
	}

	lastMessages, err := repo.GetLastMessagesByConversationIDs(ctx, conversationIDs)
	if err != nil {
		return nil, dbOperationError(err)
	}
	lastMessageByConversation := make(map[uint]model.Message, len(lastMessages))
	for _, message := range lastMessages {
		lastMessageByConversation[message.ConversationID] = message
	}

	unreadByConversation, err := repo.CountUnreadMessagesByConversationIDs(ctx, userID, conversationIDs)
	if err != nil {
		return nil, dbOperationError(err)
	}

	sessions := make([]dto.MessageSessionOutput, 0, len(conversations))
	for _, conversation := range conversations {
		session := dto.MessageSessionOutput{}
		switch conversation.Type {
		case model.ConversationTypePrivate:
			peerID, ok := peerByConversation[conversation.ID]
			if !ok {
				continue
			}
			peer, ok := peerByID[peerID]
			if !ok {
				continue
			}
			session.TargetType = dto.MessageTargetTypePrivate
			session.TargetID = peer.ID
			session.Name = peer.Nickname
			session.Avatar = peer.Avatar
		case model.ConversationTypeGroup:
			if conversation.GroupID == nil {
				continue
			}
			group, ok := groupByID[*conversation.GroupID]
			if !ok {
				continue
			}
			session.TargetType = dto.MessageTargetTypeGroup
			session.TargetID = group.ID
			session.Name = group.Name
		default:
			continue
		}

		if lastMessage, ok := lastMessageByConversation[conversation.ID]; ok {
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
		session.UnreadCount = unreadByConversation[conversation.ID]
		sessions = append(sessions, session)
	}
	sortMessageSessions(sessions)
	return sessions, nil
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
