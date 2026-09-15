package wsbus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"chat_proj/pkg/logger"

	"github.com/redis/go-redis/v9"
)

// Bus 是跨实例的 WebSocket 消息总线。多实例部署时，接收方可能连在别的实例上，
// 推送必须经过总线广播；单实例或无 Redis 时退化为进程内直投。
//
// 可靠性边界：Pub/Sub 是 fire-and-forget，订阅端掉线期间的消息不会重放。
// 消息可达性不依赖总线——消息先落库，客户端重连后用 afterMessageID 增量补拉兜底，
// 总线只负责"在线时的实时性"。
type Bus interface {
	// Publish 把 payload 推送给 userIDs 的所有在线连接（可能分布在多个实例）。
	Publish(ctx context.Context, userIDs []uint, payload any) error
	Close() error
}

// Delivery is one targeted payload inside a batch publish.
type Delivery struct {
	UserIDs []uint
	Payload any
}

// BatchBus is an optional extension used by queue consumers to amortize one
// broker round trip across a committed database batch.
type BatchBus interface {
	PublishBatch(ctx context.Context, deliveries []Delivery) error
}

// OrderedBus 是聊天消息的快速有序发布能力。它是 Bus 的可选扩展，普通通知仍使用
// Publish；调用方在单实例和 Redis 部署下都可以使用同一接口。
type OrderedBus interface {
	// PublishOrdered 把会话序号放进全局序号门，并返回已经连续发布到的实际水位。
	// publishBaseSeq 是本次分配序号时数据库已确认的水位，用于 Redis 重启后重建门。
	PublishOrdered(ctx context.Context, conversationID uint, seq, publishBaseSeq uint64, userIDs []uint, payload any) (uint64, error)
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

// Publish 通过本机 Hub 将消息推送给指定用户。
func (b *LocalBus) Publish(_ context.Context, userIDs []uint, payload any) error {
	b.hub.SendToMany(userIDs, payload)
	return nil
}

func (b *LocalBus) PublishBatch(_ context.Context, deliveries []Delivery) error {
	for _, delivery := range deliveries {
		if len(delivery.UserIDs) != 0 {
			b.hub.SendToMany(delivery.UserIDs, delivery.Payload)
		}
	}
	return nil
}

// PublishOrdered 在单实例没有跨进程乱序来源，节点内 CSP dispatcher 负责让同一会话
// 依次进入 Client.send；因此可直接投递并确认当前 seq。
func (b *LocalBus) PublishOrdered(ctx context.Context, _ uint, seq, _ uint64, userIDs []uint, payload any) (uint64, error) {
	if err := b.Publish(ctx, userIDs, payload); err != nil {
		return 0, err
	}
	return seq, nil
}

// Close 关闭本地消息总线；该实现无需释放资源。
func (b *LocalBus) Close() error {
	return nil
}

// redisChannel 是全局广播频道：每个实例都订阅，收到后只投递给本地在线的目标用户。
// 全局单频道实现简单、天然保序（Redis 按发布顺序投递给每个订阅者）；
// 代价是每个实例都会收到全量消息。实例数或消息量大之后，可以按 userID 哈希分片成多个频道。
const redisChannel = "ws:events"

const orderedGateTTL = 5 * time.Minute

// orderedPublishScript 是 Redis 内的每会话序号门。Redis 执行 Lua 时不会与其它命令
// 交错，因此 seq 乱序到达时先进入 pending hash，只有连续序号才会 PUBLISH。
// KEYS[1] 是下一条可发布序号，KEYS[2] 是等待缺口的事件；两个 key 共享 hash tag，
// 可直接用于 Redis Cluster。
var orderedPublishScript = redis.NewScript(`
local nextSeq = redis.call('GET', KEYS[1])
if not nextSeq then
  nextSeq = ARGV[1]
end
nextSeq = tonumber(nextSeq)
local seq = tonumber(ARGV[2])
if seq < nextSeq then
  redis.call('SET', KEYS[1], nextSeq, 'PX', ARGV[4])
  return nextSeq - 1
end
redis.call('HSET', KEYS[2], tostring(seq), ARGV[3])
while true do
  local encoded = redis.call('HGET', KEYS[2], tostring(nextSeq))
  if not encoded then
    break
  end
  redis.call('PUBLISH', ARGV[5], encoded)
  redis.call('HDEL', KEYS[2], tostring(nextSeq))
  nextSeq = nextSeq + 1
end
redis.call('SET', KEYS[1], nextSeq, 'PX', ARGV[4])
if redis.call('HLEN', KEYS[2]) == 0 then
  redis.call('DEL', KEYS[2])
else
  redis.call('PEXPIRE', KEYS[2], ARGV[4])
end
return nextSeq - 1
`)

type busEnvelope struct {
	UserIDs []uint          `json:"user_ids"`
	Payload json.RawMessage `json:"payload"`
}

type busBatchEnvelope struct {
	Deliveries []busEnvelope `json:"deliveries"`
}

// RedisBus 用 Redis Pub/Sub 做跨实例广播。
// 发布端不直接投递本地连接：本实例自己的订阅也会收到这条消息，投递路径保持唯一，避免本地双投。
type RedisBus struct {
	client *redis.Client
	hub    Sender
	sub    *redis.PubSub
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

// Publish 将用户列表和消息封装后发布到 Redis 跨实例频道。
func (b *RedisBus) Publish(ctx context.Context, userIDs []uint, payload any) error {
	if len(userIDs) == 0 {
		return nil
	}
	data, raw, err := marshalBusEnvelope(userIDs, payload)
	if err != nil {
		return err
	}
	if err := b.client.Publish(ctx, redisChannel, data).Err(); err != nil {
		// Redis 短暂故障时降级为本地直投：本实例上的目标用户仍能实时收到，
		// 其他实例上的用户靠重连补拉兜底。和限流的 fail-open 是同一取舍。
		logger.Warn("WSBusPublishFailed, fallback to local delivery",
			logger.String("error", err.Error()))
		b.hub.SendToMany(userIDs, json.RawMessage(raw))
		// 调用方不能把本地降级视作全局发布成功：保留未发布水位，
		// 后台协调会重试，其他实例才能收到事件。下游按会话 seq 去重。
		return err
	}
	return nil
}

// PublishBatch uses one Redis PUBLISH for an ordered list of deliveries. Every
// subscriber expands the list synchronously, so their order is identical on
// every application instance.
func (b *RedisBus) PublishBatch(ctx context.Context, deliveries []Delivery) error {
	envelopes := make([]busEnvelope, 0, len(deliveries))
	for _, delivery := range deliveries {
		if len(delivery.UserIDs) == 0 {
			continue
		}
		raw, err := json.Marshal(delivery.Payload)
		if err != nil {
			return err
		}
		envelopes = append(envelopes, busEnvelope{UserIDs: delivery.UserIDs, Payload: raw})
	}
	if len(envelopes) == 0 {
		return nil
	}
	data, err := json.Marshal(busBatchEnvelope{Deliveries: envelopes})
	if err != nil {
		return err
	}
	if err := b.client.Publish(ctx, redisChannel, data).Err(); err != nil {
		logger.Warn("WSBusBatchPublishFailed, fallback to local delivery",
			logger.String("error", err.Error()))
		for _, envelope := range envelopes {
			b.hub.SendToMany(envelope.UserIDs, json.RawMessage(envelope.Payload))
		}
		return err
	}
	return nil
}

// PublishOrdered 用 Redis Lua 做每会话的原子“暂存 + 连续放行”。这条热路径只执行
// 一次 EVAL，不再为每条消息开启数据库发布事务；不同会话的 key 完全独立，可并发运行。
func (b *RedisBus) PublishOrdered(ctx context.Context, conversationID uint, seq, publishBaseSeq uint64, userIDs []uint, payload any) (uint64, error) {
	if conversationID == 0 || seq == 0 {
		return 0, fmt.Errorf("ordered publish requires conversation and sequence")
	}
	if len(userIDs) == 0 {
		return seq, nil
	}
	data, raw, err := marshalBusEnvelope(userIDs, payload)
	if err != nil {
		return 0, err
	}
	keyPrefix := fmt.Sprintf("ws:ordered:{%d}", conversationID)
	through, err := orderedPublishScript.Run(
		ctx,
		b.client,
		[]string{keyPrefix + ":next", keyPrefix + ":pending"},
		publishBaseSeq+1,
		seq,
		string(data),
		orderedGateTTL.Milliseconds(),
		redisChannel,
	).Int64()
	if err != nil {
		// 仍尽力投递本节点的在线连接；返回 error 使数据库水位保持不变，后台恢复会
		// 重新走全局总线。节点内 dispatcher 会按 seq 去重和补洞。
		logger.Warn("WSOrderedPublishFailed, fallback to local delivery",
			logger.Uint("conversation_id", conversationID), logger.String("error", err.Error()))
		b.hub.SendToMany(userIDs, json.RawMessage(raw))
		return 0, err
	}
	if through < 0 {
		return 0, fmt.Errorf("ordered publish returned invalid watermark %d", through)
	}
	return uint64(through), nil
}

func marshalBusEnvelope(userIDs []uint, payload any) (data []byte, raw []byte, err error) {
	raw, err = json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	data, err = json.Marshal(busEnvelope{UserIDs: userIDs, Payload: raw})
	if err != nil {
		return nil, nil, err
	}
	return data, raw, nil
}

// run 消费 Redis 订阅消息并转发给本机 Hub 中的目标用户。
func (b *RedisBus) run() {
	// sub.Channel 内部处理了断线重连；Close 后通道关闭，循环退出。
	for msg := range b.sub.Channel() {
		var batch busBatchEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &batch); err == nil && len(batch.Deliveries) != 0 {
			for _, delivery := range batch.Deliveries {
				b.hub.SendToMany(delivery.UserIDs, json.RawMessage(delivery.Payload))
			}
			continue
		}
		var envelope busEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &envelope); err != nil {
			logger.Warn("WSBusInvalidEnvelope", logger.String("error", err.Error()))
			continue
		}
		// json.RawMessage 在 WriteJSON 时原样输出，不会二次转义。
		b.hub.SendToMany(envelope.UserIDs, json.RawMessage(envelope.Payload))
	}
}

// Close 停止 Redis 消息总线并关闭订阅。
func (b *RedisBus) Close() error {
	return b.sub.Close()
}
