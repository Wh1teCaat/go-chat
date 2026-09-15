# 消息保序分阶段实施计划

## 目标

在不降低消息完整性、会话内顺序和断线补拉能力的前提下，先交付现有 PostgreSQL + Redis 架构的稳定优化，再在独立分支验证 Kafka 分区保序。两个阶段使用相同的四实例压测口径，Kafka 版本只有在正确性和容量都通过后才考虑合并。

统一验收口径：四个应用实例，共享 PostgreSQL 和消息中间件；每个 WebSocket 连接每秒发送 1 条，持续 60 秒并观察 10 秒。要求写入、ACK、自身回显和对端推送完整，无重复，`SenderSequenceRegressions`、`ConversationIDRegressions`、`ConversationSeqRegressions` 均为 0。

## 阶段一：优化现有 PostgreSQL + Redis 架构

目标分支：`main`。

交付内容：

1. PostgreSQL 继续分配权威会话 `seq`，消息和序号在同一事务提交。
2. 将发布水位拆到 `conversation_publish_watermarks`，避免与 `conversations.last_seq` 竞争同一行。
3. 发布水位按会话在内存中合并，由固定数量 worker 并发刷盘。
4. Redis 发布失败立即恢复；普通水位写失败只重试水位，避免重复回放放大数据库负载。
5. 周期恢复只扫描第一条未发布消息已滞留 30 秒的会话。
6. 保留 Redis Lua 跨实例序号门、节点内 CSP dispatcher、客户端 `seq` 去重窗口和 `afterSeq` 补拉。

验收状态：2,000 连接、总 2,000 条/秒时，120,000 条全部写入和送达，无乱序与重复；对端 P99 为 771.16ms。相同剖析环境的基线 P99 为 11,395.74ms，恢复消息读取从 58,051 次降为 0。详细数据见 `loadtest/DOCKER_RESULTS.md`。

阶段一完成条件：完整 Go 测试和相关竞态测试通过，迁移可以从现有数据回填水位，压测记录入库，然后提交并推送 GitHub。

## 阶段二：Kafka 分区保序实验

目标分支：`feat/kafka-ordered-delivery`，从阶段一 GitHub 提交创建。

### 设计边界

1. Producer 以 `conversation_id` 作为 Kafka record key；同一会话只能进入同一 partition。
2. WebSocket 接入层负责鉴权、幂等参数校验和写入 Kafka，不自行产生最终 `seq`。
3. Consumer group 中每个 partition 同一时刻只由一个 consumer 处理；partition 内按 Kafka offset 顺序执行。
4. Consumer 将同一会话的连续消息组成小批次，在一个 PostgreSQL 事务内预留连续 `seq` 区间并批量插入消息。
5. 数据库提交成功后才能提交 Kafka offset；重复消费由 `sender_id + client_msg_id` 唯一约束消除。
6. 数据库提交后发布实时事件。Redis 暂时保留为在线广播层，客户端继续按 `seq` 去重和断线补拉。

### 实施步骤

1. 增加 Kafka 配置、Compose 服务、健康检查和 Go client，默认保持 Kafka 关闭。
2. 定义版本化消息 envelope，包含 `conversation_id`、发送者、目标、`client_msg_id`、内容和接入时间。
3. 实现 keyed producer，并区分 `accepted`（Kafka 已确认）与 `stored`（PostgreSQL 已提交）状态。
4. 实现 partition consumer、按会话微批、连续序号区间分配、幂等批量写库和 offset 提交。
5. 将数据库提交后的事件发送到现有实时投递层；Kafka 分支正常路径不再使用 Redis Lua 序号门和数据库发布水位。
6. 增加重启、重复消费、consumer rebalance、Kafka 暂停和 PostgreSQL 暂停测试。
7. 执行四实例 1,900、2,000、5,000 连接阶梯压测，并单独测试热点会话。

### 合并门槛

Kafka 版本必须保持全部顺序和完整性计数为 0；2,000 连接档的 P99 应低于阶段一，5,000 连接档不能出现永久丢失或不可恢复的序号缺口。还需要记录 Kafka ACK 与数据库落库之间的延迟，并明确客户端展示 `accepted` 还是 `stored`。

Kafka 实验未达到门槛时保留分支和报告，不合并到 `main`，生产继续使用阶段一方案。
