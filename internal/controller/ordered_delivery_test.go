package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"chat_proj/internal/dto"
)

type deliveryCall struct {
	userID   uint
	envelope wsEnvelope
}

type recordingDeliveryHub struct {
	mu    sync.Mutex
	calls []deliveryCall
}

func (h *recordingDeliveryHub) SendTo(userID uint, message any) bool {
	envelope, ok := message.(wsEnvelope)
	if !ok {
		return false
	}
	h.mu.Lock()
	h.calls = append(h.calls, deliveryCall{userID: userID, envelope: envelope})
	h.mu.Unlock()
	return true
}

func (h *recordingDeliveryHub) SendToMany(_ []uint, _ any) {}

func (h *recordingDeliveryHub) snapshot() []deliveryCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]deliveryCall(nil), h.calls...)
}

func TestOrderedDeliveryBuffersConversationGapBeforeClientQueues(t *testing.T) {
	hub := &recordingDeliveryHub{}
	delivery := newOrderedMessageDelivery(hub, func(context.Context, uint, uint64, int) ([]dto.OrderedMessageEvent, error) {
		return nil, errors.New("not needed by this test")
	})

	send := func(seq uint64) {
		t.Helper()
		event := dto.OrderedMessageEvent{
			ConversationID: 9,
			Seq:            seq,
			SenderID:       10,
			TargetType:     dto.MessageTargetTypePrivate,
			TargetID:       20,
			RecipientIDs:   []uint{10, 20},
			Message: dto.MessageOutput{
				ID:       uint(seq),
				Seq:      seq,
				SenderID: 10,
				Content:  "message",
			},
		}
		raw, err := json.Marshal(wsEnvelope{Type: dto.WSMessageTypeMessage, Data: event})
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		delivery.SendToMany(event.RecipientIDs, json.RawMessage(raw))
	}

	send(1)
	waitForCalls(t, hub, 2)
	send(3)
	time.Sleep(20 * time.Millisecond)
	if calls := hub.snapshot(); len(calls) != 2 {
		t.Fatalf("seq 3 bypassed gap and entered client queues: %+v", calls)
	}
	send(2)
	waitForCalls(t, hub, 6)

	byUser := map[uint][]dto.MessageOutput{}
	for _, call := range hub.snapshot() {
		message, ok := call.envelope.Data.(dto.MessageOutput)
		if !ok {
			t.Fatalf("unexpected envelope data: %T", call.envelope.Data)
		}
		byUser[call.userID] = append(byUser[call.userID], message)
	}
	for _, userID := range []uint{10, 20} {
		messages := byUser[userID]
		if len(messages) != 3 || messages[0].Seq != 1 || messages[1].Seq != 2 || messages[2].Seq != 3 {
			t.Fatalf("user %d received unordered messages: %+v", userID, messages)
		}
	}
	if byUser[10][0].TargetID != 20 || byUser[20][0].TargetID != 10 {
		t.Fatalf("private receiver view target ids are wrong: sender=%d receiver=%d", byUser[10][0].TargetID, byUser[20][0].TargetID)
	}
}

func TestOrderedDeliveryRecoversMissingSequenceWithoutBlockingPartition(t *testing.T) {
	hub := &recordingDeliveryHub{}
	delivery := newOrderedMessageDelivery(hub, func(_ context.Context, conversationID uint, afterSeq uint64, limit int) ([]dto.OrderedMessageEvent, error) {
		if conversationID != 7 || afterSeq != 1 || limit != 100 {
			return nil, errors.New("unexpected recovery request")
		}
		return []dto.OrderedMessageEvent{{
			ConversationID: 7, Seq: 2, SenderID: 10, TargetType: dto.MessageTargetTypePrivate, TargetID: 20,
			RecipientIDs: []uint{10}, Message: dto.MessageOutput{ID: 2, Seq: 2, SenderID: 10, Content: "recovered"},
		}}, nil
	})

	send := func(seq uint64) {
		t.Helper()
		event := dto.OrderedMessageEvent{
			ConversationID: 7, Seq: seq, SenderID: 10, TargetType: dto.MessageTargetTypePrivate, TargetID: 20,
			RecipientIDs: []uint{10}, Message: dto.MessageOutput{ID: uint(seq), Seq: seq, SenderID: 10, Content: "live"},
		}
		raw, err := json.Marshal(wsEnvelope{Type: dto.WSMessageTypeMessage, Data: event})
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		delivery.SendToMany(event.RecipientIDs, json.RawMessage(raw))
	}

	send(1)
	waitForCalls(t, hub, 1)
	send(3)
	waitForCalls(t, hub, 3)

	var sequences []uint64
	for _, call := range hub.snapshot() {
		message := call.envelope.Data.(dto.MessageOutput)
		sequences = append(sequences, message.Seq)
	}
	if got, want := sequences, []uint64{1, 2, 3}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("unexpected recovered output order: %v", got)
	}
}

func waitForCalls(t *testing.T, hub *recordingDeliveryHub, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(hub.snapshot()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d calls, got %d", want, len(hub.snapshot()))
}
