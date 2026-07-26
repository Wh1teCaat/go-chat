package wsbus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"chat_proj/internal/config"
)

// TestKafkaBusBroadcastsAcrossInstances 模拟两个 gateway 实例（独立消费组）+ 一个
// logic 发布端：发布的事件必须广播到两个消费实例，且 payload 原样送达。
// 需要真实 Kafka：CHAT_KAFKA_INTEGRATION=1（本机 docker compose up -d kafka 后跑）。
func TestKafkaBusBroadcastsAcrossInstances(t *testing.T) {
	if os.Getenv("CHAT_KAFKA_INTEGRATION") != "1" {
		t.Skip("set CHAT_KAFKA_INTEGRATION=1 to run real kafka integration test")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	brokers := cfg.Kafka.Brokers
	topic := fmt.Sprintf("chat.events.test.%d", time.Now().UnixNano())
	if err := EnsureTopic(context.Background(), brokers, topic, 4); err != nil {
		t.Fatalf("EnsureTopic returned error: %v", err)
	}

	sender1 := &recordingSender{}
	sender2 := &recordingSender{}
	suffix := time.Now().UnixNano()
	// NewKafkaBus 内置就绪探测：返回即代表消费组 join 完成、链路端到端可投递。
	bus1, err := NewKafkaBus(context.Background(), brokers, topic, fmt.Sprintf("test-gw1-%d", suffix), sender1)
	if err != nil {
		t.Fatalf("NewKafkaBus(1) returned error: %v", err)
	}
	defer bus1.Close()
	bus2, err := NewKafkaBus(context.Background(), brokers, topic, fmt.Sprintf("test-gw2-%d", suffix), sender2)
	if err != nil {
		t.Fatalf("NewKafkaBus(2) returned error: %v", err)
	}
	defer bus2.Close()
	publisher := NewKafkaPublisher(brokers, topic)
	defer publisher.Close()

	payload := map[string]any{"type": "message", "data": map[string]any{"id": 42}}
	if err := publisher.Publish(context.Background(), "conv:1", []uint{7}, payload); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		calls1, calls2 := sender1.snapshot(), sender2.snapshot()
		if len(calls1) >= 1 && len(calls2) >= 1 {
			for name, calls := range map[string][]recordedCall{"gw1": calls1, "gw2": calls2} {
				raw, ok := calls[0].message.(json.RawMessage)
				if !ok {
					t.Fatalf("%s: expected json.RawMessage payload, got %T", name, calls[0].message)
				}
				var decoded map[string]any
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatalf("%s: failed to decode payload: %v", name, err)
				}
				if decoded["type"] != "message" {
					t.Fatalf("%s: unexpected payload: %v", name, decoded)
				}
				if len(calls[0].userIDs) != 1 || calls[0].userIDs[0] != 7 {
					t.Fatalf("%s: unexpected user ids: %v", name, calls[0].userIDs)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for broadcast: gw1=%d gw2=%d", len(calls1), len(calls2))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestKafkaBusSameKeySamePartitionOrdering 同一顺序键的多条事件必须按发布顺序到达消费端。
func TestKafkaBusSameKeySamePartitionOrdering(t *testing.T) {
	if os.Getenv("CHAT_KAFKA_INTEGRATION") != "1" {
		t.Skip("set CHAT_KAFKA_INTEGRATION=1 to run real kafka integration test")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	brokers := cfg.Kafka.Brokers
	topic := fmt.Sprintf("chat.events.order.%d", time.Now().UnixNano())
	if err := EnsureTopic(context.Background(), brokers, topic, 4); err != nil {
		t.Fatalf("EnsureTopic returned error: %v", err)
	}

	sender := &recordingSender{}
	consumer, err := NewKafkaBus(context.Background(), brokers, topic, fmt.Sprintf("test-order-%d", time.Now().UnixNano()), sender)
	if err != nil {
		t.Fatalf("NewKafkaBus returned error: %v", err)
	}
	defer consumer.Close()
	publisher := NewKafkaPublisher(brokers, topic)
	defer publisher.Close()

	const n = 10
	for i := 0; i < n; i++ {
		payload := map[string]any{"seq": i}
		if err := publisher.Publish(context.Background(), "conv:42", []uint{1}, payload); err != nil {
			t.Fatalf("Publish %d returned error: %v", i, err)
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		calls := sender.snapshot()
		if len(calls) >= n {
			for i, call := range calls[:n] {
				var decoded map[string]any
				if err := json.Unmarshal(call.message.(json.RawMessage), &decoded); err != nil {
					t.Fatalf("decode %d: %v", i, err)
				}
				if int(decoded["seq"].(float64)) != i {
					t.Fatalf("ordering violated at %d: got seq %v", i, decoded["seq"])
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out: got %d/%d events", len(calls), n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
