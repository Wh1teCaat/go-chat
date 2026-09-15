package messagequeue

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

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
