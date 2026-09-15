package messagequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"chat_proj/internal/config"
	"chat_proj/internal/dto"
	"chat_proj/pkg/logger"

	"github.com/twmb/franz-go/pkg/kgo"
)

const commandVersion = 1
const maxPartitionBatch = 64

// MessageCommand is the durable hand-off between websocket ingress and the
// partition consumer that assigns the authoritative database sequence.
type MessageCommand struct {
	Version        int                  `json:"version"`
	ConversationID uint                 `json:"conversationID"`
	SenderID       uint                 `json:"senderID"`
	Input          dto.SendMessageInput `json:"input"`
	EnqueuedAt     time.Time            `json:"enqueuedAt"`
}

type Handler func(context.Context, []MessageCommand) error

type Queue interface {
	Enqueue(context.Context, MessageCommand) error
	Ping(context.Context) error
	Close() error
}

// KafkaQueue produces commands and consumes the same topic as part of one
// consumer group. Kafka assigns each partition to one application instance;
// records inside a partition are handled serially while different partitions
// are processed concurrently.
type KafkaQueue struct {
	client  *kgo.Client
	topic   string
	handler Handler
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

func NewKafkaQueue(parent context.Context, cfg config.KafkaConfig, handler Handler) (*KafkaQueue, error) {
	if handler == nil {
		return nil, errors.New("kafka message handler is required")
	}
	if len(cfg.Brokers) == 0 || cfg.Topic == "" || cfg.GroupID == "" {
		return nil, errors.New("kafka brokers, topic and group_id are required")
	}
	options := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.GroupID),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.BlockRebalanceOnPoll(),
		kgo.FetchMinBytes(1 << 20),
		kgo.FetchMaxWait(10 * time.Millisecond),
		kgo.ProducerLinger(2 * time.Millisecond),
	}
	if cfg.ClientID != "" {
		options = append(options, kgo.ClientID(cfg.ClientID))
	}
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	queue := &KafkaQueue{
		client:  client,
		topic:   cfg.Topic,
		handler: handler,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go queue.consume(ctx)
	return queue, nil
}

func (q *KafkaQueue) Enqueue(ctx context.Context, command MessageCommand) error {
	if command.ConversationID == 0 || command.SenderID == 0 {
		return errors.New("kafka message command requires conversation and sender")
	}
	command.Version = commandVersion
	if command.EnqueuedAt.IsZero() {
		command.EnqueuedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return err
	}
	record := &kgo.Record{
		Topic: q.topic,
		Key:   []byte(strconv.FormatUint(uint64(command.ConversationID), 10)),
		Value: payload,
	}
	return q.client.ProduceSync(ctx, record).FirstErr()
}

func (q *KafkaQueue) Ping(ctx context.Context) error {
	return q.client.Ping(ctx)
}

func (q *KafkaQueue) Close() error {
	q.once.Do(func() {
		q.cancel()
		<-q.done
		q.client.Close()
	})
	return nil
}

func (q *KafkaQueue) consume(ctx context.Context) {
	defer close(q.done)
	for ctx.Err() == nil {
		fetches := q.client.PollRecords(ctx, 512)
		if ctx.Err() != nil {
			q.client.AllowRebalance()
			return
		}
		for _, fetchErr := range fetches.Errors() {
			logger.Warn("KafkaMessageFetchFailed",
				logger.String("topic", fetchErr.Topic),
				logger.Any("partition", fetchErr.Partition),
				logger.String("error", fetchErr.Err.Error()))
		}

		records := fetches.Records()
		if len(records) == 0 {
			q.client.AllowRebalance()
			continue
		}

		lastRecords := q.processRecords(ctx, records)
		if len(lastRecords) > 0 && ctx.Err() == nil {
			if err := q.client.CommitRecords(ctx, lastRecords...); err != nil {
				logger.Warn("KafkaMessageCommitFailed", logger.String("error", err.Error()))
			}
		}
		q.client.AllowRebalance()
	}
}

func (q *KafkaQueue) processRecords(ctx context.Context, records []*kgo.Record) []*kgo.Record {
	lastByPartition := make(map[int32]*kgo.Record)
	for start := 0; start < len(records); {
		commands := make([]MessageCommand, 0, maxPartitionBatch)
		batchRecords := make([]*kgo.Record, 0, maxPartitionBatch)
		for start < len(records) && len(commands) < maxPartitionBatch {
			record := records[start]
			start++
			var command MessageCommand
			if err := json.Unmarshal(record.Value, &command); err != nil || command.Version != commandVersion || command.ConversationID == 0 {
				logger.Error("KafkaMessageInvalid",
					logger.Any("partition", record.Partition),
					logger.Any("offset", record.Offset),
					logger.String("error", fmt.Sprint(err)))
				lastByPartition[record.Partition] = record // poison records cannot block the partition forever.
				continue
			}
			commands = append(commands, command)
			batchRecords = append(batchRecords, record)
		}
		if len(commands) == 0 {
			continue
		}
		for {
			if err := q.handler(ctx, commands); err == nil {
				for _, record := range batchRecords {
					lastByPartition[record.Partition] = record
				}
				break
			} else {
				logger.Warn("KafkaMessageHandleRetry",
					logger.Uint("conversation_id", commands[0].ConversationID),
					logger.Any("partition", batchRecords[0].Partition),
					logger.Any("offset", batchRecords[0].Offset),
					logger.Any("batch_size", len(commands)),
					logger.String("error", err.Error()))
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return recordsFromPartitionMap(lastByPartition)
			case <-timer.C:
			}
		}
	}
	return recordsFromPartitionMap(lastByPartition)
}

func recordsFromPartitionMap(lastByPartition map[int32]*kgo.Record) []*kgo.Record {
	records := make([]*kgo.Record, 0, len(lastByPartition))
	for _, record := range lastByPartition {
		records = append(records, record)
	}
	return records
}
