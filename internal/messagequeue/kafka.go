package messagequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
const defaultConsumerWorkers = 4

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
	workers int
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
		workers: normalizedWorkerCount(cfg.ConsumerWorkers),
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

// processRecords assigns each partition to one bounded worker. A worker handles
// the records of its partition in offset order; different partitions can write
// PostgreSQL and publish Redis events concurrently. The returned offset for a
// partition never advances beyond its last successfully handled raw record.
func (q *KafkaQueue) processRecords(ctx context.Context, records []*kgo.Record) []*kgo.Record {
	byPartition := make(map[int32][]*kgo.Record)
	for _, record := range records {
		byPartition[record.Partition] = append(byPartition[record.Partition], record)
	}

	partitions := make([]int32, 0, len(byPartition))
	for partition := range byPartition {
		partitions = append(partitions, partition)
	}
	sort.Slice(partitions, func(i, j int) bool { return partitions[i] < partitions[j] })

	type partitionResult struct {
		partition int32
		last      *kgo.Record
	}
	jobs := make(chan int32)
	results := make(chan partitionResult, len(partitions))
	workers := normalizedWorkerCount(q.workers)
	if workers > len(partitions) {
		workers = len(partitions)
	}

	var workerGroup sync.WaitGroup
	workerGroup.Add(workers)
	for range workers {
		go func() {
			defer workerGroup.Done()
			for partition := range jobs {
				results <- partitionResult{
					partition: partition,
					last:      q.processPartition(ctx, byPartition[partition]),
				}
			}
		}()
	}
	for _, partition := range partitions {
		jobs <- partition
	}
	close(jobs)
	workerGroup.Wait()
	close(results)

	lastByPartition := make(map[int32]*kgo.Record, len(partitions))
	for result := range results {
		if result.last != nil {
			lastByPartition[result.partition] = result.last
		}
	}
	return recordsFromPartitionMap(lastByPartition)
}

// processPartition is deliberately serial: Kafka keying ensures all commands
// for one conversation land here, so invoking the handler out of offset order
// would violate the conversation sequence guarantee.
func (q *KafkaQueue) processPartition(ctx context.Context, records []*kgo.Record) *kgo.Record {
	var last *kgo.Record
	for start := 0; start < len(records); {
		end := start + maxPartitionBatch
		if end > len(records) {
			end = len(records)
		}
		rawBatch := records[start:end]
		start = end

		commands := make([]MessageCommand, 0, len(rawBatch))
		for _, record := range rawBatch {
			var command MessageCommand
			if err := json.Unmarshal(record.Value, &command); err != nil || command.Version != commandVersion || command.ConversationID == 0 {
				logger.Error("KafkaMessageInvalid",
					logger.Any("partition", record.Partition),
					logger.Any("offset", record.Offset),
					logger.String("error", fmt.Sprint(err)))
				continue
			}
			commands = append(commands, command)
		}
		if len(commands) == 0 {
			// Poison records cannot block this partition forever.
			last = rawBatch[len(rawBatch)-1]
			continue
		}

		for {
			if err := q.handler(ctx, commands); err == nil {
				// The handler covered every valid command before rawBatch's final
				// record, so committing this offset also skips any poison record
				// that appeared between them.
				last = rawBatch[len(rawBatch)-1]
				break
			} else {
				logger.Warn("KafkaMessageHandleRetry",
					logger.Uint("conversation_id", commands[0].ConversationID),
					logger.Any("partition", rawBatch[0].Partition),
					logger.Any("offset", rawBatch[0].Offset),
					logger.Any("batch_size", len(commands)),
					logger.String("error", err.Error()))
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return last
			case <-timer.C:
			}
		}
	}
	return last
}

func normalizedWorkerCount(workers int) int {
	if workers <= 0 {
		return defaultConsumerWorkers
	}
	return workers
}

func recordsFromPartitionMap(lastByPartition map[int32]*kgo.Record) []*kgo.Record {
	records := make([]*kgo.Record, 0, len(lastByPartition))
	for _, record := range lastByPartition {
		records = append(records, record)
	}
	return records
}
