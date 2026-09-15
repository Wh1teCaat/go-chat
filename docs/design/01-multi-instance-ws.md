# 阶段 1：WebSocket 多实例化（跨节点消息路由）

## 动因

单体版的 `ws.Hub` 是进程内 map：用户 A 连在实例 1、好友 B 连在实例 2 时，实例 1 上的推送找不到 B 的连接，消息只能等 B 下次拉取。Redis Pub/Sub 用来打通这些实例之间的实时推送。

## 方案

引入 `internal/wsbus.Bus` 抽象，推送不再直接调用本地 Hub。聊天消息在阶段 2 增加会话 seq 和 Redis 序号门，完整设计见[会话有序投递](02-ordered-delivery.md)：

```text
普通通知 ──▶ pushToUsers ──▶ Bus.Publish
聊天消息 ──▶ 事务内落库/分配 seq ──▶ Redis Lua 序号门 ──▶ PUBLISH ws:events
                                                       │
                         ┌─────────────────────────────┴──────────────────────────────┐
                    LocalBus（单实例）                     RedisBus（多实例）
                    节点内 CSP dispatcher              每个实例 SUBSCRIBE 后进入 CSP dispatcher
                                                                   │
                                                        本地在线目标连接的 Client.send
```

- **ACK / error 不走总线**：它们只对发起发送的那个连接有意义（靠 clientMsgID 对上本地"发送中"的消息），而发起连接必然在处理请求的这个实例上，本地直投即可。
- **消息本体 / 已读回执 / 好友申请 / 入群申请走总线**：目标用户可能连在任何实例。
- **发布端不做本地直投**：本实例自己的订阅也会收到消息，投递路径保持唯一，避免双投。

## 关键取舍

**全局单频道 vs 按用户分频道。** 按用户分频道（`ws:user:<id>`）只让持有该用户连接的实例收到消息，更省带宽，但需要在连接建立/断开时动态订阅/退订，还要处理订阅生效前的消息竞态。全局单频道让每个实例收全量消息、只投本地在线用户，实现简单，并保持订阅端观察到的 Redis 发布顺序。当前规模选后者；实例数或消息量上来后，可按 userID 哈希分片成 N 个频道渐进优化，不需要推翻设计。

Redis Pub/Sub 是 fire-and-forget：订阅端掉线期间的消息直接丢失，也没有消费位点。这里能接受，因为**可达性不依赖总线**——消息先落库，客户端断线重连后用会话 `afterSeq` 增量补拉兜底，总线只负责"在线时的实时性"。

**总线故障降级。** `RedisBus.Publish` 失败时降级为本地直投并告警：本实例上的目标用户仍实时收到，其他实例上的用户靠重连补拉。与限流的 fail-open 是同一取舍——基础设施抖动不应打断聊天主链路。

## 时序与幂等

- 普通 Pub/Sub 只保证发布顺序，不保证并发事务的数据库 ID 顺序。聊天消息在进入 Pub/Sub 前由 Redis Lua 按会话 seq 暂存、连续放行，再由节点内 CSP dispatcher 写入 `Client.send`；压测以 `ConversationSeqRegressions=0` 作为通过条件。实测见 [Docker 报告](../../loadtest/DOCKER_RESULTS.md)。
- 客户端对推送按 `id`/`clientMsgID` 去重，并以最后连续 `seq` 补拉；总线层不需要 exactly-once。

## 配套

- 在线状态早已在 Redis（presence store），多实例下天然一致。
- 优雅停机顺序：停 HTTP 监听 → 退订总线 → 关闭本地 WS 连接。
- 集成测试 `TestRedisBusBroadcastsAcrossInstances` 用两个 Bus 实例模拟两个节点，验证跨实例送达（`CHAT_REDIS_INTEGRATION=1` 门控）。

## 当前限制

- 限流的内存实现和 upload 清理任务在多实例下会各自为政（限流退化为每实例独立配额、清理任务重复执行但幂等）；上 Redis 后限流自动共享，清理任务可加分布式锁。
- 本地磁盘存储（uploads/）多实例下不共享，需要先换 MinIO/OSS 才能真正多实例部署文件功能。
