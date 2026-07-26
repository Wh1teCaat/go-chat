package wsbus

import (
	"context"
	"encoding/json"

	"chat_proj/pkg/logger"

	"github.com/redis/go-redis/v9"
)

// Bus 是跨实例的 WebSocket 消息总线。多实例部署时，接收方可能连在别的实例上，
// 推送必须经过总线广播；单实例或无 Redis 时退化为进程内直投。
//
// 可靠性边界：总线只负责"在线时的实时性"，消息可达性不依赖总线——
// 消息先落库，客户端重连后用 afterMessageID 增量补拉兜底。
// Redis Pub/Sub 实现是 fire-and-forget；Kafka 实现有持久化和消费位点，
// 但订阅端（gateway）以 latest 位点加入，语义上仍按"掉线期间靠补拉"设计。
type Bus interface {
	// Publish 把 payload 推送给 userIDs 的所有在线连接（可能分布在多个实例）。
	// key 声明事件的顺序域（同 key 的事件保序投递），如 "conv:<会话ID>"；
	// Kafka 实现用它做分区键，Local/Redis 实现全局有序、忽略该参数。
	Publish(ctx context.Context, key string, userIDs []uint, payload any) error
	Close() error
}

// Sender 是投递端需要的最小 Hub 能力，*ws.Hub 天然满足。
type Sender interface {
	SendToMany(userIDs []uint, message any)
}

// LocalBus 进程内直投，用于单实例部署和测试。
type LocalBus struct {
	hub Sender
}

func NewLocalBus(hub Sender) *LocalBus {
	return &LocalBus{hub: hub}
}

func (b *LocalBus) Publish(_ context.Context, _ string, userIDs []uint, payload any) error {
	b.hub.SendToMany(userIDs, payload)
	return nil
}

func (b *LocalBus) Close() error {
	return nil
}

// redisChannel 是全局广播频道：每个实例都订阅，收到后只投递给本地在线的目标用户。
// 全局单频道实现简单、天然保序（Redis 按发布顺序投递给每个订阅者）；
// 代价是每个实例都会收到全量消息。实例数或消息量大之后，可以按 userID 哈希分片成多个频道。
const redisChannel = "ws:events"

type busEnvelope struct {
	UserIDs []uint          `json:"user_ids"`
	Payload json.RawMessage `json:"payload"`
	// Probe 标记就绪探针事件：KafkaBus 启动时用它验证发布→消费链路，消费端不投递。
	Probe bool `json:"probe,omitempty"`
}

// RedisBus 用 Redis Pub/Sub 做跨实例广播。
// 发布端不直接投递本地连接：本实例自己的订阅也会收到这条消息，投递路径保持唯一，避免本地双投。
type RedisBus struct {
	client *redis.Client
	hub    Sender
	sub    *redis.PubSub
}

// NewRedisPublisher 只发布、不订阅，用于自身不持有 WebSocket 连接的服务（chat-logic）：
// 它产生推送但从不投递，投递发生在订阅了频道的 gateway 实例上。
// fallback 传 nil 时，Redis 故障期间的消息只能靠客户端重连补拉兜底。
func NewRedisPublisher(client *redis.Client, fallback Sender) *RedisBus {
	return &RedisBus{client: client, hub: fallback}
}

func NewRedisBus(ctx context.Context, client *redis.Client, hub Sender) (*RedisBus, error) {
	sub := client.Subscribe(ctx, redisChannel)
	// 等订阅确认后再返回，避免启动早期发布的消息落在订阅生效之前。
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, err
	}

	b := &RedisBus{client: client, hub: hub, sub: sub}
	go b.run()
	return b, nil
}

func (b *RedisBus) Publish(ctx context.Context, _ string, userIDs []uint, payload any) error {
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
	if err := b.client.Publish(ctx, redisChannel, data).Err(); err != nil {
		// Redis 短暂故障时降级为本地直投：本实例上的目标用户仍能实时收到，
		// 其他实例上的用户靠重连补拉兜底。和限流的 fail-open 是同一取舍。
		logger.Warn("WSBusPublishFailed, fallback to local delivery",
			logger.String("error", err.Error()))
		if b.hub != nil {
			b.hub.SendToMany(userIDs, json.RawMessage(raw))
		}
	}
	return nil
}

func (b *RedisBus) run() {
	// sub.Channel 内部处理了断线重连；Close 后通道关闭，循环退出。
	for msg := range b.sub.Channel() {
		var envelope busEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &envelope); err != nil {
			logger.Warn("WSBusInvalidEnvelope", logger.String("error", err.Error()))
			continue
		}
		// json.RawMessage 在 WriteJSON 时原样输出，不会二次转义。
		b.hub.SendToMany(envelope.UserIDs, json.RawMessage(envelope.Payload))
	}
}

func (b *RedisBus) Close() error {
	if b.sub == nil {
		return nil
	}
	return b.sub.Close()
}
