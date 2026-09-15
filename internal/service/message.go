package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"chat_proj/internal/repository"
	"chat_proj/pkg/apperrors"

	"gorm.io/gorm"
)

type messageService struct {
	asyncCommit atomic.Bool
}

var MessageService = new(messageService)

// InitMessageAsyncCommit 配置聊天消息事务的持久性策略。默认关闭，保持 PostgreSQL
// 同步提交；开启后 ACK 不等待 WAL fsync，可降低 Docker/网络盘等慢存储上的尾延迟。
func InitMessageAsyncCommit(enabled bool) {
	MessageService.asyncCommit.Store(enabled)
}

type ConversationMessageResult struct {
	Message        dto.MessageOutput
	ConversationID uint
	PublishBaseSeq uint64
	ReceiverIDs    []uint
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
}

// OrderedMessagePublisher 把一条已提交的规范化消息事件交给实时总线。
// 调用方应只在 publish 成功后允许发布水位推进。
type OrderedMessagePublisher func(ctx context.Context, event dto.OrderedMessageEvent) error

// SendConversationMessage 校验会话成员和消息内容，持久化消息并返回接收者信息。
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
	var publishBaseSeq uint64
	err = repo.WithTransaction(func(tx *repository.Repository) error {
		if s.asyncCommit.Load() {
			if err := tx.EnableAsyncCommitForTransaction(ctx); err != nil {
				return err
			}
		}
		seq, publishedSeq, err := tx.ReserveNextConversationSeq(ctx, conversation.ID)
		if err != nil {
			return err
		}
		message.Seq = seq
		publishBaseSeq = publishedSeq
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

	// 私聊已由 resolveConversation 的双成员 JOIN 同时完成权限校验，接收者就是 targetID，
	// 无需在每条消息提交后再扫一次 conversation_members。群聊仍读取当前成员集合。
	receiverIDs := []uint{input.TargetID}
	if input.TargetType != dto.MessageTargetTypePrivate {
		receiverIDs, err = s.conversationReceiverIDs(ctx, conversation.ID, senderID)
		if err != nil {
			return nil, err
		}
	}

	return &ConversationMessageResult{
		Message:        toMessageOutput(*message),
		ConversationID: conversation.ID,
		PublishBaseSeq: publishBaseSeq,
		ReceiverIDs:    receiverIDs,
		ClientMsgID:    clientMsgID,
		TargetType:     input.TargetType,
		TargetID:       input.TargetID,
	}, nil
}

// duplicateMessageResult 校验幂等消息内容并构造已有消息的发送结果。
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

// conversationReceiverIDs 返回会话中除发送者之外的成员 ID。
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

// PublishPendingConversationMessages 按会话 seq 串行发布已经提交但尚未确认发布的消息。
// 会话行锁让多个实例可以共同触发恢复，而同一会话只会有一个实例按序调用 publisher。
// publisher 在事务内执行：先成功交给总线、后推进水位。发布成功后水位写入失败会重发，
// 因此下游必须按 seq 去重；发布失败则保持水位不变，留给下一次恢复任务。
func (s *messageService) PublishPendingConversationMessages(ctx context.Context, conversationID uint, publisher OrderedMessagePublisher) error {
	if publisher == nil {
		return apperrors.ErrInvalidInput
	}

	const maxEventsPerDrain = 128
	return repo.WithTransaction(func(tx *repository.Repository) error {
		conversation, err := tx.GetConversationForUpdate(ctx, conversationID)
		if err != nil {
			return err
		}
		members, err := tx.ListConversationMembersByConversationID(ctx, conversationID)
		if err != nil {
			return err
		}

		for published := 0; published < maxEventsPerDrain; published++ {
			next := conversation.LastPublishedSeq + 1
			if next > conversation.LastSeq {
				return nil
			}
			message, err := tx.GetMessageByConversationAndSeq(ctx, conversationID, next)
			if err != nil {
				return err
			}
			event, err := orderedMessageEvent(*conversation, *message, members)
			if err != nil {
				return err
			}
			if err := publisher(ctx, event); err != nil {
				return err
			}
			if err := tx.AdvanceConversationPublishedSeq(ctx, conversationID, conversation.LastPublishedSeq, next); err != nil {
				return err
			}
			conversation.LastPublishedSeq = next
		}
		return nil
	})
}

// ListConversationsWithUnpublishedMessages 返回需要后台重试实时发布的会话。
func (s *messageService) ListConversationsWithUnpublishedMessages(ctx context.Context, limit int) ([]uint, error) {
	ids, err := repo.ListConversationIDsWithUnpublishedMessages(ctx, limit)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return ids, nil
}

// HasUnpublishedConversationMessages 供 publisher 在释放本机去重标记后决定是否立即续排。
func (s *messageService) HasUnpublishedConversationMessages(ctx context.Context, conversationID uint) (bool, error) {
	has, err := repo.HasUnpublishedMessages(ctx, conversationID)
	if err != nil {
		return false, dbOperationError(err)
	}
	return has, nil
}

// MarkConversationMessagesPublished 确认 Redis 的会话序号门已经连续放行到 seq。
// 该更新允许跨越多条消息：例如 seq=6 先到 Redis 等待，随后 seq=5 到达时 Lua 会一次
// 放行 5、6，并把实际连续水位 6 返回给调用方。
func (s *messageService) MarkConversationMessagesPublished(ctx context.Context, conversationID uint, seq uint64) error {
	if conversationID == 0 || seq == 0 {
		return nil
	}
	if err := repo.AdvanceConversationPublishedSeqAtLeast(ctx, conversationID, seq); err != nil {
		return dbOperationError(err)
	}
	return nil
}

// LoadOrderedMessageEvents 从权威存储补齐某个会话连续游标之后的消息。
// 它只供节点内 dispatcher 在 Redis 通知出现缺口时使用，不执行用户权限查询；
// 事件仍只会经本机 Hub 投递给当前在线的连接。
func (s *messageService) LoadOrderedMessageEvents(ctx context.Context, conversationID uint, afterSeq uint64, limit int) ([]dto.OrderedMessageEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	conversation, err := repo.GetConversationByID(ctx, conversationID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	members, err := repo.ListConversationMembersByConversationID(ctx, conversationID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	messages, err := repo.ListMessagesAfterSeq(ctx, conversationID, afterSeq, limit)
	if err != nil {
		return nil, dbOperationError(err)
	}
	events := make([]dto.OrderedMessageEvent, 0, len(messages))
	for _, message := range messages {
		event, err := orderedMessageEvent(*conversation, message, members)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func orderedMessageEvent(conversation model.Conversation, message model.Message, members []model.ConversationMember) (dto.OrderedMessageEvent, error) {
	event := dto.OrderedMessageEvent{
		ConversationID: conversation.ID,
		Seq:            message.Seq,
		SenderID:       message.SenderID,
		Message:        toMessageOutput(message),
	}
	for _, member := range members {
		event.RecipientIDs = append(event.RecipientIDs, member.UserID)
	}

	switch conversation.Type {
	case model.ConversationTypePrivate:
		event.TargetType = dto.MessageTargetTypePrivate
		for _, member := range members {
			if member.UserID != message.SenderID {
				event.TargetID = member.UserID
				break
			}
		}
		if event.TargetID == 0 {
			return dto.OrderedMessageEvent{}, apperrors.ErrInvalidInput
		}
	case model.ConversationTypeGroup:
		if conversation.GroupID == nil || *conversation.GroupID == 0 {
			return dto.OrderedMessageEvent{}, apperrors.ErrInvalidInput
		}
		event.TargetType = dto.MessageTargetTypeGroup
		event.TargetID = *conversation.GroupID
	default:
		return dto.OrderedMessageEvent{}, apperrors.ErrInvalidInput
	}
	return event, nil
}

type fileMessageContent struct {
	Kind string `json:"kind"`
	ID   uint   `json:"id"`
}

// fileIDFromMessageContent 从文件消息 JSON 内容中解析文件 ID。
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

// ListMessages 校验会话访问权限并分页返回消息记录。
func (s *messageService) ListMessages(ctx context.Context, userID uint, input dto.ListMessagesInput) ([]dto.MessageOutput, error) {
	if input.AfterMessageID > 0 && input.AfterSeq > 0 {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "afterMessageID and afterSeq are mutually exclusive")
	}
	conversation, err := s.resolveConversation(ctx, userID, input.TargetType, input.TargetID)
	if err != nil {
		return nil, err
	}

	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// afterSeq 是有序消息的连续游标；兼容客户端仍可使用 afterMessageID。
	if input.AfterSeq > 0 {
		messages, err := repo.ListMessagesAfterSeq(ctx, conversation.ID, input.AfterSeq, limit)
		if err != nil {
			return nil, dbOperationError(err)
		}
		return toMessageOutputs(messages), nil
	}
	// afterMessageID 用于旧版客户端的断线重连增量补拉。
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

// MarkMessageRead 推进用户在会话中的已读位置并返回需要通知的成员。
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

// formatMessageTime 将消息时间格式化为统一的 UTC RFC3339 字符串。
func formatMessageTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// formatOptionalTime 安全格式化可为空的时间值。
func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatMessageTime(*t)
}

// toMessageOutput 将消息模型转换为对外输出结构。
func toMessageOutput(message model.Message) dto.MessageOutput {
	output := dto.MessageOutput{
		ID:        message.ID,
		Seq:       message.Seq,
		SenderID:  message.SenderID,
		Content:   message.Content,
		CreatedAt: formatMessageTime(message.CreatedAt),
	}
	if message.ClientMsgID != nil {
		output.ClientMsgID = *message.ClientMsgID
	}
	return output
}

// toMessageOutputs 批量将消息模型转换为对外输出结构。
func toMessageOutputs(messages []model.Message) []dto.MessageOutput {
	result := make([]dto.MessageOutput, 0, len(messages))
	for _, message := range messages {
		result = append(result, toMessageOutput(message))
	}
	return result
}

// sortMessageSessions 按最后消息时间和消息 ID 对会话摘要倒序排列。
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

// lastMessageID 返回会话摘要中的最后消息 ID，空会话返回零。
func lastMessageID(session dto.MessageSessionOutput) uint {
	if session.LastMessage == nil {
		return 0
	}
	return session.LastMessage.ID
}

// resolveConversation 根据私聊或群聊目标查找或创建用户可用的会话。
func (s *messageService) resolveConversation(ctx context.Context, userID uint, targetType dto.MessageTargetType, targetID uint) (*model.Conversation, error) {
	// 客户端只传 targetType/targetID。这里把目标解析成内部 conversation，
	// 并完成成员校验；被删好友或退群后不会再有权限发消息或看历史。
	switch targetType {
	case dto.MessageTargetTypePrivate:
		conversation, err := repo.GetPrivateConversationBetweenUsers(ctx, userID, targetID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, apperrors.WithMessage(apperrors.ErrNotFound, "private conversation not found")
			}
			return nil, dbOperationError(err)
		}
		return conversation, nil
	case dto.MessageTargetTypeGroup:
		conversation, err := repo.GetConversationByGroupID(ctx, targetID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, apperrors.WithMessage(apperrors.ErrNotFound, "group conversation not found")
			}
			return nil, dbOperationError(err)
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
