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
	if err := bus.Publish(context.Background(), "conv:1", []uint{1, 2}, payload); err != nil {
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
	if err := bus1.Publish(context.Background(), "conv:7", []uint{7}, payload); err != nil {
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
