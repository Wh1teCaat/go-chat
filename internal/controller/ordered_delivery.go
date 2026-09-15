package controller

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/internal/wsbus"
	"chat_proj/pkg/logger"
)

const orderedDeliveryPartitions = 16
const orderedDeliveryQueueSize = 1024
const publishedWatermarkFlushInterval = 100 * time.Millisecond

// orderedMessageDelivery 是节点内的 CSP 投递层。每个 conversationID 固定落入一个
// goroutine；该 goroutine 是同一会话消息写入本机 Client.send 的唯一入口。
type orderedMessageDelivery struct {
	hub        orderedDeliveryHub
	partitions []chan orderedDeliveryInput
	recover    orderedMessageRecovery
}

type orderedMessageRecovery func(ctx context.Context, conversationID uint, afterSeq uint64, limit int) ([]dto.OrderedMessageEvent, error)

type orderedDeliveryInput struct {
	event          *dto.OrderedMessageEvent
	conversationID uint
	recovered      []dto.OrderedMessageEvent
	recoveryDone   bool
	recoveryFailed bool
}

type orderedConversationState struct {
	next       uint64
	pending    map[uint64]dto.OrderedMessageEvent
	recovering bool
}

type orderedDeliveryHub interface {
	SendTo(userID uint, message any) bool
	SendToMany(userIDs []uint, message any)
}

func newOrderedMessageDelivery(hub orderedDeliveryHub) *orderedMessageDelivery {
	return newOrderedMessageDeliveryWithRecovery(hub, service.MessageService.LoadOrderedMessageEvents)
}

func newOrderedMessageDeliveryWithRecovery(hub orderedDeliveryHub, recover orderedMessageRecovery) *orderedMessageDelivery {
	d := &orderedMessageDelivery{hub: hub, recover: recover, partitions: make([]chan orderedDeliveryInput, orderedDeliveryPartitions)}
	for i := range d.partitions {
		d.partitions[i] = make(chan orderedDeliveryInput, orderedDeliveryQueueSize)
		go d.runPartition(d.partitions[i])
	}
	return d
}

// SendToMany 先识别规范化聊天事件；其它事件（ACK 以外的通知、已读回执等）沿用直接投递。
// Redis 和 LocalBus 都传递 JSON，所以两个部署模式经过完全相同的有序分支。
func (d *orderedMessageDelivery) SendToMany(userIDs []uint, message any) {
	raw, ok := message.(json.RawMessage)
	if !ok {
		encoded, err := json.Marshal(message)
		if err != nil {
			logger.Warn("WSDeliveryEncodeFailed", logger.String("error", err.Error()))
			return
		}
		raw = encoded
	}

	var envelope struct {
		Type dto.WSMessageType `json:"type"`
		Data json.RawMessage   `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Type != dto.WSMessageTypeMessage {
		d.hub.SendToMany(userIDs, raw)
		return
	}

	var event dto.OrderedMessageEvent
	if err := json.Unmarshal(envelope.Data, &event); err != nil || event.ConversationID == 0 || event.Seq == 0 {
		// 兼容旧格式消息：该分支只会在滚动升级的短暂窗口使用，不能把它伪装成有序消息。
		d.hub.SendToMany(userIDs, raw)
		return
	}
	if len(event.RecipientIDs) == 0 {
		event.RecipientIDs = append([]uint(nil), userIDs...)
	}

	d.partitions[int(event.ConversationID%uint(len(d.partitions)))] <- orderedDeliveryInput{event: &event}
}

func (d *orderedMessageDelivery) runPartition(inbox <-chan orderedDeliveryInput) {
	states := make(map[uint]*orderedConversationState)

	for input := range inbox {
		if input.recoveryDone {
			current := states[input.conversationID]
			if current == nil {
				continue
			}
			current.recovering = false
			if input.recoveryFailed {
				continue
			}
			for _, event := range input.recovered {
				d.accept(current, event)
			}
			d.recoverGap(current, input.conversationID)
			continue
		}
		if input.event == nil {
			continue
		}
		event := *input.event
		current := states[event.ConversationID]
		if current == nil {
			// 实例启动前的历史已由客户端 REST 同步处理。Redis 订阅确认后本实例看到的
			// 第一条是该会话的新基线；之后依赖全局发布水位保持连续。
			current = &orderedConversationState{next: event.Seq, pending: make(map[uint64]dto.OrderedMessageEvent)}
			states[event.ConversationID] = current
		}
		d.accept(current, event)
		d.recoverGap(current, event.ConversationID)
	}
}

func (d *orderedMessageDelivery) accept(current *orderedConversationState, event dto.OrderedMessageEvent) {
	if event.Seq < current.next {
		return // 发布水位写入失败后的重发，或 Redis 重连后的迟到事件。
	}
	current.pending[event.Seq] = event
	for {
		next, ok := current.pending[current.next]
		if !ok {
			return
		}
		d.deliver(next)
		delete(current.pending, current.next)
		current.next++
	}
}

// recoverGap 仅在发现 seq 缺口时启动一个异步数据库读取。查询结果被投回原分区，
// 所以 pending/next 始终由同一 goroutine 修改；正常路径不会产生这次查询。
func (d *orderedMessageDelivery) recoverGap(current *orderedConversationState, conversationID uint) {
	if current.recovering || len(current.pending) == 0 {
		return
	}
	if _, hasNext := current.pending[current.next]; hasNext {
		return
	}
	current.recovering = true
	partition := d.partitions[int(conversationID%uint(len(d.partitions)))]
	afterSeq := current.next - 1
	go func() {
		events, err := d.recover(context.Background(), conversationID, afterSeq, 100)
		if err != nil {
			logger.Warn("WSOrderedDeliveryRecoveryFailed", logger.Uint("conversation_id", conversationID), logger.String("error", err.Error()))
		}
		partition <- orderedDeliveryInput{
			conversationID: conversationID,
			recovered:      events,
			recoveryDone:   true,
			recoveryFailed: err != nil,
		}
	}()
}

func (d *orderedMessageDelivery) deliver(event dto.OrderedMessageEvent) {
	for _, userID := range event.RecipientIDs {
		message := event.Message
		message.Seq = event.Seq
		message.TargetType = event.TargetType
		message.TargetID = event.TargetID
		if event.TargetType == dto.MessageTargetTypePrivate && userID != event.SenderID {
			message.TargetID = event.SenderID
		}
		if userID != event.SenderID {
			message.ClientMsgID = ""
		}
		d.hub.SendTo(userID, wsEnvelope{Type: dto.WSMessageTypeMessage, Data: message})
	}
}

// orderedMessagePublisher 从持久化发布水位取出消息并协调 Redis 发布。单实例内通过
// 固定分区减少 goroutine 和锁竞争；跨实例顺序由 PublishPendingConversationMessages 的
// conversations 行锁保证。
type orderedMessagePublisherType struct {
	partitions []chan uint
	once       sync.Once
	mu         sync.Mutex
	queued     map[uint]struct{}
}

func newOrderedMessagePublisher() *orderedMessagePublisherType {
	return &orderedMessagePublisherType{
		partitions: make([]chan uint, orderedDeliveryPartitions),
		queued:     make(map[uint]struct{}),
	}
}

func (p *orderedMessagePublisherType) Start(ctx context.Context) {
	p.once.Do(func() {
		for i := range p.partitions {
			p.partitions[i] = make(chan uint, orderedDeliveryQueueSize)
			go p.runPartition(ctx, p.partitions[i])
		}
		go p.recover(ctx)
	})
}

func (p *orderedMessagePublisherType) Enqueue(conversationID uint) {
	if conversationID == 0 {
		return
	}
	// 单元测试和嵌入式调用没有 main 生命周期时，在首次真实消息到达后启动。
	p.Start(context.Background())
	p.mu.Lock()
	if _, exists := p.queued[conversationID]; exists {
		p.mu.Unlock()
		return
	}
	p.queued[conversationID] = struct{}{}
	p.mu.Unlock()
	inbox := p.partitions[int(conversationID%uint(len(p.partitions)))]
	select {
	case inbox <- conversationID:
	default:
		p.clearQueued(conversationID)
		// 发布水位仍在数据库，定时恢复会重试；不为每次入队额外创建 goroutine。
		logger.Warn("WSOrderedPublisherQueueFull", logger.Uint("conversation_id", conversationID))
	}
}

func (p *orderedMessagePublisherType) runPartition(ctx context.Context, inbox <-chan uint) {
	for {
		select {
		case <-ctx.Done():
			return
		case conversationID := <-inbox:
			// 这里只处理 Redis 故障、发布水位写入失败等少见恢复场景。正常发送路径
			// 直接走 Redis Lua 序号门，不能再让每条消息串行经过数据库事务。
			err := service.MessageService.PublishPendingConversationMessages(context.Background(), conversationID, publishPendingOrderedEvent)
			// 先清除本机去重标记，再查水位：这期间提交的新消息若已尝试 Enqueue，
			// 会由下面的水位检查补回；之后提交的消息则会自行成功入队。
			p.clearQueued(conversationID)
			if err != nil {
				logger.Warn("WSOrderedPublishFailed", logger.Uint("conversation_id", conversationID), logger.String("error", err.Error()))
				continue
			}
			hasMore, err := service.MessageService.HasUnpublishedConversationMessages(context.Background(), conversationID)
			if err != nil {
				logger.Warn("WSOrderedPublishWatermarkCheckFailed", logger.Uint("conversation_id", conversationID), logger.String("error", err.Error()))
				continue
			}
			if hasMore {
				p.Enqueue(conversationID)
			}
		}
	}
}

func (p *orderedMessagePublisherType) clearQueued(conversationID uint) {
	p.mu.Lock()
	delete(p.queued, conversationID)
	p.mu.Unlock()
}

func (p *orderedMessagePublisherType) recover(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := service.MessageService.ListConversationsWithUnpublishedMessages(context.Background(), 256)
			if err != nil {
				logger.Warn("WSOrderedPublishRecoveryScanFailed", logger.String("error", err.Error()))
				continue
			}
			for _, id := range ids {
				p.Enqueue(id)
			}
		}
	}
}

// orderedPublishedWatermarkFlusher 将 Redis 已连续放行的水位合并后异步写回数据库。
// 正常路径每条消息都必须经过 Redis Lua，但不需要再同步等待一次 PostgreSQL UPDATE。
// 进程在这段短窗口崩溃时，恢复任务会从较旧水位重发少量事件；dispatcher 按 seq 去重。
type orderedPublishedWatermarkFlusher struct {
	once    sync.Once
	mu      sync.Mutex
	pending map[uint]uint64
}

func newOrderedPublishedWatermarkFlusher() *orderedPublishedWatermarkFlusher {
	return &orderedPublishedWatermarkFlusher{pending: make(map[uint]uint64)}
}

func (f *orderedPublishedWatermarkFlusher) Start(ctx context.Context) {
	f.once.Do(func() {
		go f.run(ctx)
	})
}

// Confirm records the largest confirmed contiguous seq for one conversation. It only mutates a
// small in-memory map in the request goroutine; actual DB work is batched by run.
func (f *orderedPublishedWatermarkFlusher) Confirm(conversationID uint, seq uint64) {
	if conversationID == 0 || seq == 0 {
		return
	}
	f.Start(context.Background())
	f.mu.Lock()
	if seq > f.pending[conversationID] {
		f.pending[conversationID] = seq
	}
	f.mu.Unlock()
}

func (f *orderedPublishedWatermarkFlusher) run(ctx context.Context) {
	ticker := time.NewTicker(publishedWatermarkFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			f.flush(context.Background())
			return
		case <-ticker.C:
			f.flush(ctx)
		}
	}
}

func (f *orderedPublishedWatermarkFlusher) flush(parent context.Context) {
	f.mu.Lock()
	confirmed := f.pending
	f.pending = make(map[uint]uint64)
	f.mu.Unlock()

	for conversationID, seq := range confirmed {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		err := service.MessageService.MarkConversationMessagesPublished(ctx, conversationID, seq)
		cancel()
		if err == nil {
			continue
		}
		logger.Warn("WSOrderedPublishWatermarkDeferred",
			logger.Uint("conversation_id", conversationID),
			logger.Any("published_through", seq),
			logger.String("error", err.Error()))
		f.Confirm(conversationID, seq)
		orderedMessagePublisher.Enqueue(conversationID)
	}
}

// publishPendingOrderedEvent 是数据库恢复路径。调用者已持有该 conversation 的行锁并
// 按 seq 顺序遍历，因此普通 Publish 已能保持全局发布顺序；节点 dispatcher 仍会处理
// 水位写入失败造成的重复事件。
func publishPendingOrderedEvent(ctx context.Context, event dto.OrderedMessageEvent) error {
	return publishEnvelope(ctx, event.RecipientIDs, wsEnvelope{Type: dto.WSMessageTypeMessage, Data: event})
}

// publishOrderedEvent 是热路径：一次 Redis Lua 调用完成暂存、连续放行和 Pub/Sub。
// 返回值是 Redis 已实际放行的连续水位，必须据此而非假设 seq 成功来确认数据库水位。
func publishOrderedEvent(ctx context.Context, event dto.OrderedMessageEvent) (uint64, error) {
	raw, err := json.Marshal(wsEnvelope{Type: dto.WSMessageTypeMessage, Data: event})
	if err != nil {
		return 0, err
	}
	if orderedBus, ok := wsBus.(wsbus.OrderedBus); ok {
		return orderedBus.PublishOrdered(
			ctx,
			event.ConversationID,
			event.Seq,
			event.PublishBaseSeq,
			event.RecipientIDs,
			json.RawMessage(raw),
		)
	}
	if err := wsBus.Publish(ctx, event.RecipientIDs, json.RawMessage(raw)); err != nil {
		return 0, err
	}
	return event.Seq, nil
}

var orderedDelivery = newOrderedMessageDelivery(WSHub)
var orderedMessagePublisher = newOrderedMessagePublisher()
var orderedPublishedWatermarks = newOrderedPublishedWatermarkFlusher()

// InitOrderedMessageProcessing 由 main 在 service 与 wsBus 都就绪后调用。
func InitOrderedMessageProcessing(ctx context.Context) {
	orderedMessagePublisher.Start(ctx)
	orderedPublishedWatermarks.Start(ctx)
}

// OrderedDeliverySender 供 LocalBus 和 RedisBus 使用，保证两者先通过会话分区 dispatcher。
func OrderedDeliverySender() wsbus.Sender {
	return orderedDelivery
}
