# 聊天消息数据库写路径优化计划

> 后续会话从本文件继续。先完成“基线与剖析”，再决定实施哪一项；不要因为压测结果直接删除会话 `seq`、Redis Lua 序号门、CSP dispatcher 或断线补拉。

## 目标与当前结论

目标是在保持跨实例会话有序、ACK/对端推送完整和断线补拉正确的前提下，提高低延迟容量。验收线：60 秒持续负载、发送相位打散、无写入/业务/读错误、ACK/自身回显/对端投递完整、无重复、`ConversationSeqRegressions=0`，对端 P99 不超过 200ms。

异步提交已经开启：`configs/config.toml` 的 `database.message_async_commit = true`。它只在 `SendConversationMessage` 的事务内执行 `SET LOCAL synchronous_commit = 'off'`；已确认它降低了 WAL fsync 带来的尾延迟，但主机断电时可能丢失极少量已 ACK 的新消息。

2026-09-11 的四实例 Docker 结果：

- 每连接每秒 1 条时，1,900 个 WebSocket 连接（每实例 475、总 1,900 条/秒）全部送达，对端 P99 **144.11ms**。
- 2,000 个连接（总 2,000 条/秒）全部送达但 P99 **343.85ms**，越过验收线。
- 5,000 个连接（总 5,000 条/秒）出现写错误、读断开和数十秒积压。
- 5,000 连接时应用实例各约 145MiB、PostgreSQL 约 415MiB，而 CPU 已明显繁忙；这不是内存耗尽。Redis CPU 低于 PostgreSQL 和应用，Redis 不是首个瓶颈。

完整结果见 `loadtest/DOCKER_RESULTS.md`。该数字是本机 Docker 观测，不能直接作为服务器容量承诺。

## 不能破坏的正确性边界

1. `messages(conversation_id, seq)` 是会话内权威顺序，且有唯一约束；全局 `messages.id` 不能作为会话顺序。
2. `conversations.last_seq` 的分配与消息写入必须在同一事务中。相同会话的并发发送必须得到连续、唯一的 `seq`。
3. Redis Lua 序号门可以暂存先到的高序号事件，且只发布连续序列。消费者仍需按 `seq` 去重。
4. 本节点 CSP dispatcher 负责实例内交付顺序；跨实例通过 Redis Pub/Sub；断线通过 REST `afterSeq` 补拉。
5. `last_published_seq` 是实时发布的恢复水位。Redis 发布成功而水位落库失败时允许重发，不能丢失消息。

涉及文件：

- `internal/service/message.go`：消息事务、恢复发布和水位确认。
- `internal/repository/conversation.go`：`ReserveNextConversationSeq` 与水位 SQL。
- `internal/controller/ordered_delivery.go`：分区 dispatcher、100ms 水位合并刷盘、恢复扫描。
- `internal/wsbus/bus.go`：Redis Lua 序号门。
- `migrations/005_add_conversation_message_sequence.sql`：会话序号/水位模式。
- `loadtest/main.go`、`loadtest/compose.yml`：真实 HTTP/WebSocket 多实例压测。

## 第一阶段：先定位，禁止盲改

在空的专用 Compose 数据库，四实例、每实例 `max_open_conns=20` 下，分别运行 1,800、1,900、2,000 条/秒（每连接 1 条/秒、60 秒）。不要使用生产库。

每一档同时采集：

1. PostgreSQL `pg_stat_activity`：按 `wait_event_type` / `wait_event` 统计，特别关注 `Lock`、`LWLock`、`WALWrite`、`WALSync`、`ClientRead`。
2. PostgreSQL `pg_stat_statements`：按 `total_exec_time`、`mean_exec_time`、调用次数、行数，找出 `conversations` 的行锁读取/更新、消息 INSERT、发布水位 UPDATE、私聊会话查询。
3. `EXPLAIN (ANALYZE, BUFFERS)`：对 `ReserveNextConversationSeq` 的会话锁读取、消息 INSERT 所涉索引、私聊查询、`last_published_seq` 合并更新分别采样。
4. Go `pprof`：CPU、heap、goroutine、mutex/block profile；同时记录四个实例和 Redis/PostgreSQL 的 Docker CPU、内存、网络、磁盘 I/O。
5. Redis `INFO commandstats` 和 Lua 调用耗时，确认它仍不是瓶颈。

产物放入 `loadtest/results/<timestamp>/`，并将命令、容器镜像、配置、原始 JSON、采样结果写进同目录。只有一项 SQL 或锁等待在 2,000 条/秒时占主要时间，才针对它改动。

## 第二阶段：按证据实施

### A. 若发布水位 UPDATE 或恢复扫描占主要时间（优先、低风险）

当前 `orderedPublishedWatermarkFlusher` 已按 100ms 合并同一会话的水位，但每个会话仍单独 UPDATE，恢复路径还会读写会话行。

**已验证并回退的尝试（2026-09-15）：** 将同批多个会话合并为 PostgreSQL `UPDATE ... FROM (VALUES ...)`。在四实例、2,000 连接、每连接每秒 1 条的 60 秒测试中，P99 从此前 343.85ms 恶化到约 23 秒，并出现连接读写错误。大语句同时锁定大量会话行，扩大了锁竞争；不要再次实施该方案，除非 profile 证明锁行为已改变。

实施：

1. 采样确认正常发送路径没有为每条消息触发 `HasUnpublishedConversationMessages`、恢复事务或额外水位 UPDATE。
2. 将一次 flush 的多个 `(conversation_id, seq)` 合并为一条参数化批量 UPDATE，例如通过 `VALUES` 表执行 `last_published_seq = GREATEST(...)`；限制每批条数与 SQL 参数数。
3. 写入失败时把整批或失败项放回 pending map；不能静默丢弃。保留 100ms 刷盘和停机强制 flush。
4. 给恢复扫描增加限速、指标和分页游标，避免大量正常会话每 2 秒被重复扫描。

测试：批量 SQL 单元测试、失败重试测试、跨实例重发去重测试、既有顺序测试和 1,900/2,000 条每秒回归。

### B. 若消息事务的会话行锁/`last_seq` UPDATE 是主要时间（中风险）

不要直接使用 Redis INCR 代替数据库序号；实例崩溃后未使用的序号会留下永久缺口，Redis Lua 门会阻塞该会话。

先实施低风险动作：

1. 用单条 `UPDATE conversations SET last_seq = last_seq + 1 ... RETURNING last_seq, last_published_seq` 替换“SELECT FOR UPDATE 再 UPDATE”，但只在专用基准中对比。此前尝试未提升本机结果，必须以新 profile 为准。
2. 确认 `conversations(id)` 主键和消息唯一索引命中，且没有触发器/无关索引放大写入。
3. 对热点单会话与多会话分别压测；多会话容量和单群热点的优化方案不同。

如果仍需“序号区间租约”，先写设计和故障模型，再编码：租约必须持久化、未使用区间必须有可推进/跳过的明确状态，发布器必须能区分真正缺失与已废弃序号。此方案不进入本轮快速优化。

### C. 若消息 INSERT / 索引写放大为主（中风险）

1. 核查 `messages` 的索引集合和写放大，删除未被读取路径使用的冗余索引前先以 `pg_stat_user_indexes` 取证。
2. 检查 autovacuum、checkpoint、WAL、磁盘 IOPS；服务器使用本地 NVMe 时应重新测量。
3. 仅在性能证据充分时评估按时间或会话分区。分区不能破坏 `(conversation_id, seq)` 唯一性、历史查询、迁移和清理任务。

### D. 若 Go dispatcher / WebSocket 写出为主（低至中风险）

1. 用 CPU、mutex、block profile 确认分区数量、共享 map、JSON 编解码或慢客户端写出的位置。
2. 保持“同会话固定分区、连接单写协程”的顺序模型；只能优化队列容量、批量取队列、缓冲复用和慢连接隔离。
3. 不允许为提升并发而让同一会话事件跨多个 dispatcher 并行写出。

## 发布与验收

每个小改动单独提交以下内容：代码、必要迁移、单元/竞态测试、压测原始结果、`loadtest/DOCKER_RESULTS.md` 的结论。最低验证命令：

```bash
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test ./...
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test -race ./internal/service ./internal/controller ./internal/wsbus ./internal/database
```

端到端至少复测：

- 1,900、2,000 个连接，每连接每秒 1 条，四实例。
- 热点会话：少量会话、多发送者并发。
- Redis 短暂不可用、发布水位写失败、节点重启、客户端断线后 `afterSeq` 补拉。

成功标准不是只提高峰值吞吐：2,000 连接档需 P99 低于 200ms，且所有正确性计数保持 0。若优化未达标，保留 profile 和结果，回滚该小改动，不削弱有序/恢复语义。

## 部署注意事项

- 四个应用实例使用 PostgreSQL 默认 `max_connections=100` 时，每实例连接池设为 20；否则 `4 × 30` 会触发 `too many clients`。生产要么统一下调连接池，要么按数据库容量提高 `max_connections`，并为管理连接留余量。
- `message_async_commit=true` 仅适用于可接受极少量已 ACK 消息在主机断电时丢失的场景。关键交易、账号等事务保持同步提交。
- 目标服务器容量必须由独立压测机、真实网络、实际磁盘、固定 CPU/内存限额和更长稳态压测重新确认。
