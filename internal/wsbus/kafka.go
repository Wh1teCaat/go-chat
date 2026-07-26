package wsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"chat_proj/pkg/logger"

	"github.com/segmentio/kafka-go"
)

// KafkaBus 用 Kafka 做跨实例事件总线，替代 Redis Pub/Sub 的动因：
//  1. 持久化 + 消费位点：事件不再是 fire-and-forget，可以重放、可以审计；
//  2. 多消费组：同一份事件流可以同时喂给 gateway 推送、离线推送、消息轨迹等
//     互不干扰的消费者，Pub/Sub 做不到"新用途不改老链路"。
//
// 顺序与幂等：按 Publish 的 key 哈希分区，同一顺序域（如同一会话）的事件
// 落在同一分区、分区内严格有序；投递语义是 at-least-once，重复投递由
// 客户端按消息 ID / clientMsgID 去重兜底（阶段 0 已具备）。
type KafkaBus struct {
	writer *kafka.Writer
	// reader 只在消费端（gateway）存在；纯发布端（logic）为 nil。
	reader *kafka.Reader
	hub    Sender
	done   chan struct{}
	ready  chan struct{}
	closed atomic.Bool
}

// publishTimeout 是单次发布的上限。kafka-go Writer 默认 10 次重试 × 10s 超时，
// broker 故障时会把调用方（REST handler / gRPC 里的推送编排）挂住分钟级；
// 收紧到秒级让"降级本地直投"的承诺及时兑现，事件由客户端补拉兜底。
const publishTimeout = 3 * time.Second

// NewKafkaPublisher 只发布不消费，用于 chat-logic：它产生事件但从不投递，
// 投递发生在各 gateway 实例的消费循环里。
func NewKafkaPublisher(brokers []string, topic string) *KafkaBus {
	return &KafkaBus{writer: newKafkaWriter(brokers, topic)}
}

// NewKafkaBus 创建消费端总线（gateway）：
// groupID 必须每次启动全局唯一——Kafka 的消费组内是"分摊"语义，而推送需要
// "广播"语义（每个 gateway 都要收到全量事件，再各自过滤本地在线用户），
// 所以每实例每次启动一个全新消费组，latest 位点确定生效：重启不重放积压，
// 掉线补偿走客户端 afterMessageID 补拉。空闲旧组由 broker 的 offsets 保留策略清理。
//
// 返回前会用探针事件验证"发布→消费"链路端到端就绪（消费组 join + 分区分配完成），
// 与 RedisBus 等订阅确认后再返回的启动语义对齐；否则 gateway 开始接受连接后、
// 消费组就绪前发布的事件会因 latest 位点被整体跳过，在线用户静默漏推。
func NewKafkaBus(ctx context.Context, brokers []string, topic, groupID string, hub Sender) (*KafkaBus, error) {
	b := &KafkaBus{
		writer: newKafkaWriter(brokers, topic),
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:     brokers,
			Topic:       topic,
			GroupID:     groupID,
			StartOffset: kafka.LastOffset,
			MinBytes:    1,
			MaxBytes:    1 << 20,
			// 消费快、事件小，短提交间隔降低重复投递窗口（客户端有幂等兜底）。
			CommitInterval: time.Second,
		}),
		hub:   hub,
		done:  make(chan struct{}),
		ready: make(chan struct{}),
	}
	go b.run()

	if err := b.waitReady(ctx); err != nil {
		_ = b.Close()
		return nil, err
	}
	return b, nil
}

// waitReady 反复发布探针事件（Probe 标记，消费端不投递），直到本实例的消费循环
// 收到任意一条，证明 join/rebalance 完成且 latest 位点已解析到探针之前。
func (b *KafkaBus) waitReady(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	probe, _ := json.Marshal(busEnvelope{Probe: true})
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		// 探针写失败不致命（broker 可能还在选主），下一轮继续。
		_ = b.writer.WriteMessages(waitCtx, kafka.Message{Key: []byte("probe"), Value: probe})
		select {
		case <-b.ready:
			return nil
		case <-waitCtx.Done():
			return fmt.Errorf("kafka consumer not ready within 30s (group join timeout): %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func newKafkaWriter(brokers []string, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:  kafka.TCP(brokers...),
		Topic: topic,
		// Hash balancer：按 message key 哈希选分区，同 key 严格同分区
		// （前提是分区数不变；扩分区会改变映射，见 docs/design/03 遗留）。
		Balancer: &kafka.Hash{},
		// 单副本 demo 下 RequireOne 足够；同步写让失败在本次调用内暴露并触发降级。
		RequiredAcks: kafka.RequireOne,
		BatchTimeout: 5 * time.Millisecond,
		// 默认 10 次重试 × 10s/次会在故障时挂住调用方，收紧到快速失败。
		WriteTimeout: publishTimeout,
		MaxAttempts:  2,
	}
}

func (b *KafkaBus) Publish(ctx context.Context, key string, userIDs []uint, payload any) error {
	if len(userIDs) == 0 {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data, err := json.Marshal(busEnvelope{UserIDs: userIDs, Payload: raw})
	if err != nil {
		return err
	}
	// 调用方（REST handler）的 ctx 往往没有 deadline，必须自己兜上限，
	// 否则 broker 故障会把请求路径挂住到 Writer 重试耗尽。
	writeCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	err = b.writer.WriteMessages(writeCtx, kafka.Message{Key: []byte(key), Value: data})
	if err != nil {
		// 与 RedisBus 同一取舍：总线短暂故障降级本地直投（消费端实例还能覆盖本地用户），
		// 纯发布端（hub 为 nil）依赖客户端重连补拉兜底。降级后不再上抛错误。
		// 注意超时后后台批可能仍写入成功 → 事件重复投递，由客户端幂等去重兜底。
		logger.Warn("KafkaBusPublishFailed, fallback to local delivery",
			logger.String("key", key),
			logger.String("error", err.Error()))
		if b.hub != nil {
			b.hub.SendToMany(userIDs, json.RawMessage(raw))
		}
	}
	return nil
}

func (b *KafkaBus) run() {
	defer close(b.done)
	readySignaled := false
	for {
		msg, err := b.reader.ReadMessage(context.Background())
		if err != nil {
			// EOF/Canceled 这类哨兵错误在 broker 抖动时也可能沿包装链冒出来，
			// 不能凭错误类型判断"是不是在关闭"——只信显式的 closed 标志。
			// 非关闭场景一律记日志继续循环（Reader 内部会自愈重连），
			// 保证消费 goroutine 除 Close 外没有任何静默退出的路径。
			if b.closed.Load() {
				return
			}
			logger.Warn("KafkaBusReadFailed", logger.String("error", err.Error()))
			// 避免在持续故障时空转刷日志。
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var envelope busEnvelope
		if err := json.Unmarshal(msg.Value, &envelope); err != nil {
			logger.Warn("KafkaBusInvalidEnvelope", logger.String("error", err.Error()))
			continue
		}
		// 收到任意事件（含探针）说明消费链路已打通。探针只用于就绪确认，不投递。
		if !readySignaled {
			readySignaled = true
			close(b.ready)
		}
		if envelope.Probe {
			continue
		}
		b.hub.SendToMany(envelope.UserIDs, json.RawMessage(envelope.Payload))
	}
}

func (b *KafkaBus) Close() error {
	b.closed.Store(true)
	var errs []error
	if b.reader != nil {
		if err := b.reader.Close(); err != nil {
			errs = append(errs, err)
		}
		// 等消费循环退出，避免关闭过程中还向 hub 投递。
		select {
		case <-b.done:
		case <-time.After(3 * time.Second):
		}
	}
	if b.writer != nil {
		if err := b.writer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// EnsureTopic 幂等地创建事件 topic。logic 和 gateway 启动时都会调用，
// 避免对启动顺序有依赖；已存在时直接返回。
func EnsureTopic(ctx context.Context, brokers []string, topic string, partitions int) error {
	if len(brokers) == 0 {
		return fmt.Errorf("kafka brokers not configured")
	}
	if partitions <= 0 {
		partitions = 8
	}
	client := &kafka.Client{Addr: kafka.TCP(brokers...), Timeout: 10 * time.Second}
	resp, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{
		Topics: []kafka.TopicConfig{{
			Topic:             topic,
			NumPartitions:     partitions,
			ReplicationFactor: 1,
		}},
	})
	if err != nil {
		return err
	}
	if topicErr := resp.Errors[topic]; topicErr != nil && !errors.Is(topicErr, kafka.TopicAlreadyExists) {
		return topicErr
	}
	return nil
}
