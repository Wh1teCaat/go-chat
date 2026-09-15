package messagequeue

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"chat_proj/internal/dto"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestProcessRecordsPreservesPartitionOrderAcrossRetry(t *testing.T) {
	commands := []MessageCommand{
		{Version: commandVersion, ConversationID: 7, SenderID: 1, Input: dto.SendMessageInput{ClientMsgID: "first"}},
		{Version: commandVersion, ConversationID: 7, SenderID: 2, Input: dto.SendMessageInput{ClientMsgID: "second"}},
	}
	records := make([]*kgo.Record, 0, len(commands))
	for offset, command := range commands {
		payload, err := json.Marshal(command)
		if err != nil {
			t.Fatalf("marshal command: %v", err)
		}
		records = append(records, &kgo.Record{Partition: 3, Offset: int64(offset), Value: payload})
	}

	var mu sync.Mutex
	var handled []string
	firstAttempts := 0
	queue := &KafkaQueue{handler: func(_ context.Context, commands []MessageCommand) error {
		mu.Lock()
		defer mu.Unlock()
		if commands[0].Input.ClientMsgID == "first" {
			firstAttempts++
			if firstAttempts == 1 {
				return errors.New("temporary database failure")
			}
		}
		for _, command := range commands {
			handled = append(handled, command.Input.ClientMsgID)
		}
		return nil
	}}

	last := queue.processRecords(context.Background(), records)
	if len(last) != 1 || last[0].Offset != 1 {
		t.Fatalf("last processed offsets = %v, want [1]", last)
	}
	if !reflect.DeepEqual(handled, []string{"first", "second"}) {
		t.Fatalf("partition order = %v", handled)
	}
	if firstAttempts != 2 {
		t.Fatalf("first record attempts = %d, want 2", firstAttempts)
	}
}

func TestProcessRecordsSkipsPoisonRecordAndContinues(t *testing.T) {
	valid, err := json.Marshal(MessageCommand{
		Version: commandVersion, ConversationID: 8, SenderID: 1,
	})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	var handled int
	queue := &KafkaQueue{handler: func(_ context.Context, commands []MessageCommand) error {
		handled += len(commands)
		return nil
	}}
	records := []*kgo.Record{
		{Partition: 1, Offset: 4, Value: []byte("not-json")},
		{Partition: 1, Offset: 5, Value: valid},
	}
	last := queue.processRecords(context.Background(), records)
	if len(last) != 1 || last[0].Offset != 5 || handled != 1 {
		t.Fatalf("last=%v handled=%d", last, handled)
	}
}

func TestProcessRecordsRunsPartitionsConcurrentlyAndKeepsEachOrder(t *testing.T) {
	makeRecord := func(partition int32, offset int64, clientMsgID string) *kgo.Record {
		t.Helper()
		payload, err := json.Marshal(MessageCommand{
			Version: commandVersion, ConversationID: uint(partition + 1), SenderID: 1,
			Input: dto.SendMessageInput{ClientMsgID: clientMsgID},
		})
		if err != nil {
			t.Fatalf("marshal command: %v", err)
		}
		return &kgo.Record{Partition: partition, Offset: offset, Value: payload}
	}

	started := make(chan int32, 2)
	release := make(chan struct{})
	var mu sync.Mutex
	handled := make(map[int32][]string)
	queue := &KafkaQueue{
		workers: 2,
		handler: func(_ context.Context, commands []MessageCommand) error {
			partition := int32(commands[0].ConversationID - 1)
			started <- partition
			<-release
			mu.Lock()
			defer mu.Unlock()
			for _, command := range commands {
				handled[partition] = append(handled[partition], command.Input.ClientMsgID)
			}
			return nil
		},
	}
	records := []*kgo.Record{
		makeRecord(1, 0, "p1-first"),
		makeRecord(2, 0, "p2-first"),
		makeRecord(1, 1, "p1-second"),
		makeRecord(2, 1, "p2-second"),
	}
	done := make(chan []*kgo.Record, 1)
	go func() { done <- queue.processRecords(context.Background(), records) }()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("partitions were not handled concurrently")
		}
	}
	close(release)
	last := <-done
	if len(last) != 2 {
		t.Fatalf("last offsets = %v, want one per partition", last)
	}
	if !reflect.DeepEqual(handled[1], []string{"p1-first", "p1-second"}) ||
		!reflect.DeepEqual(handled[2], []string{"p2-first", "p2-second"}) {
		t.Fatalf("partition order = %#v", handled)
	}
}
