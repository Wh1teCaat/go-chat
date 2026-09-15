package wsbus

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"chat_proj/internal/cache"
	"chat_proj/internal/config"
	"chat_proj/pkg/logger"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	logger.Logger = zap.NewNop()
	os.Exit(m.Run())
}

type recordingSender struct {
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	userIDs []uint
	message any
}

func (s *recordingSender) SendToMany(userIDs []uint, message any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedCall{userIDs: userIDs, message: message})
}

func (s *recordingSender) snapshot() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedCall(nil), s.calls...)
}

func TestLocalBusDeliversDirectly(t *testing.T) {
	sender := &recordingSender{}
	bus := NewLocalBus(sender)
	defer bus.Close()

	payload := map[string]string{"type": "message"}
	if err := bus.Publish(context.Background(), []uint{1, 2}, payload); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(calls))
	}
	if len(calls[0].userIDs) != 2 || calls[0].userIDs[0] != 1 || calls[0].userIDs[1] != 2 {
		t.Fatalf("unexpected user ids: %v", calls[0].userIDs)
	}
}

func TestLocalBusDeliversBatchInOrder(t *testing.T) {
	sender := &recordingSender{}
	bus := NewLocalBus(sender)
	if err := bus.PublishBatch(context.Background(), []Delivery{
		{UserIDs: []uint{1}, Payload: "first"},
		{UserIDs: []uint{2}, Payload: "second"},
	}); err != nil {
		t.Fatalf("PublishBatch returned error: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 2 || calls[0].message != "first" || calls[1].message != "second" {
		t.Fatalf("unexpected ordered batch: %#v", calls)
	}
}

// TestRedisBusBroadcastsAcrossInstances 模拟两个实例：各自有独立的 Hub（Sender）和总线，
// 实例 1 发布的消息必须能通过 Redis 送达实例 2 的本地投递端。
func TestRedisBusBroadcastsAcrossInstances(t *testing.T) {
	if os.Getenv("CHAT_REDIS_INTEGRATION") != "1" {
		t.Skip("set CHAT_REDIS_INTEGRATION=1 to run real redis integration test")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if !cfg.Redis.Enabled {
		t.Fatal("redis is disabled by config")
	}
	client, err := cache.NewRedisClient(context.Background(), cfg.Redis)
	if err != nil {
		t.Fatalf("NewRedisClient returned error: %v", err)
	}
	defer client.Close()

	sender1 := &recordingSender{}
	sender2 := &recordingSender{}
	bus1, err := NewRedisBus(context.Background(), client, sender1)
	if err != nil {
		t.Fatalf("NewRedisBus(1) returned error: %v", err)
	}
	defer bus1.Close()
	bus2, err := NewRedisBus(context.Background(), client, sender2)
	if err != nil {
		t.Fatalf("NewRedisBus(2) returned error: %v", err)
	}
	defer bus2.Close()

	payload := map[string]any{"type": "message", "data": map[string]any{"id": 42}}
	if err := bus1.Publish(context.Background(), []uint{7}, payload); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		calls1, calls2 := sender1.snapshot(), sender2.snapshot()
		// 发布端实例也通过自己的订阅收到消息（投递路径唯一，不做本地双投）。
		if len(calls1) >= 1 && len(calls2) >= 1 {
			raw, ok := calls2[0].message.(json.RawMessage)
			if !ok {
				t.Fatalf("expected json.RawMessage payload, got %T", calls2[0].message)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("failed to decode payload: %v", err)
			}
			if decoded["type"] != "message" {
				t.Fatalf("unexpected payload: %v", decoded)
			}
			if len(calls2[0].userIDs) != 1 || calls2[0].userIDs[0] != 7 {
				t.Fatalf("unexpected user ids: %v", calls2[0].userIDs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cross-instance delivery: instance1=%d instance2=%d", len(calls1), len(calls2))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRedisBusOrderedPublishBuffersOutOfOrderSequence 覆盖跨实例最容易出现的反转：
// seq=2 先抵达 Redis 时不得发布，seq=1 到达后两条必须按 1、2 进入每个实例。
func TestRedisBusOrderedPublishBuffersOutOfOrderSequence(t *testing.T) {
	mini, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mini.Close()

	client1 := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer client1.Close()
	client2 := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer client2.Close()

	sender1 := &recordingSender{}
	sender2 := &recordingSender{}
	bus1, err := NewRedisBus(context.Background(), client1, sender1)
	if err != nil {
		t.Fatalf("NewRedisBus(1): %v", err)
	}
	defer bus1.Close()
	bus2, err := NewRedisBus(context.Background(), client2, sender2)
	if err != nil {
		t.Fatalf("NewRedisBus(2): %v", err)
	}
	defer bus2.Close()

	publish := func(seq uint64) uint64 {
		t.Helper()
		through, err := bus1.PublishOrdered(context.Background(), 99, seq, 0, []uint{7}, map[string]any{
			"type": "message", "data": map[string]any{"seq": seq},
		})
		if err != nil {
			t.Fatalf("publish seq %d: %v", seq, err)
		}
		return through
	}

	if through := publish(2); through != 0 {
		t.Fatalf("seq 2 must wait for the gap, got watermark %d", through)
	}
	time.Sleep(20 * time.Millisecond)
	if got := len(sender1.snapshot()) + len(sender2.snapshot()); got != 0 {
		t.Fatalf("seq 2 published before seq 1: %d deliveries", got)
	}
	if through := publish(1); through != 2 {
		t.Fatalf("expected seq 1 to release through 2, got %d", through)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(sender1.snapshot()) == 2 && len(sender2.snapshot()) == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for name, calls := range map[string][]recordedCall{"instance1": sender1.snapshot(), "instance2": sender2.snapshot()} {
		if len(calls) != 2 {
			t.Fatalf("%s got %d deliveries", name, len(calls))
		}
		for i, call := range calls {
			raw, ok := call.message.(json.RawMessage)
			if !ok {
				t.Fatalf("%s message %d type = %T, want json.RawMessage", name, i, call.message)
			}
			var payload struct {
				Data struct {
					Seq uint64 `json:"seq"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode %s message %d: %v", name, i, err)
			}
			if want := uint64(i + 1); payload.Data.Seq != want {
				t.Fatalf("%s delivery %d seq = %d, want %d", name, i, payload.Data.Seq, want)
			}
		}
	}
}
