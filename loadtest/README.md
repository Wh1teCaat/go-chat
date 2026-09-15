# 多实例 WebSocket 压测

此工具会注册真实账号、建立好友关系并持久化消息，请只对独立测试数据库运行。账号创建、登录、建会话和连接建立不计入消息测量时间。它测的是已连接用户并发收发，不是登录吞吐或连接建立速率。

## 运行

从仓库根目录执行：

```bash
docker compose -p gochat-bench -f loadtest/compose.yml up --build -d
go build -o /tmp/gochat-loadtest ./loadtest
/tmp/gochat-loadtest -urls http://127.0.0.1:18081,http://127.0.0.1:18082 \
  -pairs 50 -messages 200 -rate 20 -spread -drain 5s -output /tmp/100-connections.json
```

也可以在容器健康后运行完整 Docker 测试矩阵（5 档短测及 2 档 60 秒持续负载），同时保存容器资源采样：

```bash
python loadtest/run-docker.py --output loadtest/results/docker-my-run
```

输出目录必须尚不存在。脚本使用上面的 `/tmp/gochat-loadtest`，不会自动停止容器；运行完后按下文命令停止专用环境。CPU 百分比按 Docker 口径，100% 约为一个逻辑核；资源采样标记了账号准备和消息测量阶段。

两个端点必须对应不同后端进程，共享数据库与 Redis。连接交替分配至两个端点，保证每个私聊会话的两个发送者跨实例。也可以使用单个 nginx 入口，但必须通过 `X-Upstream-Addr` 响应头确认连接分布；报告中 `CrossInstancePairs` 给出实际跨实例会话数，为零则报错。直连测试不覆盖 nginx 的容量与延迟。

专用 Compose 配置关闭 HTTP 限流，避免单 IP 批量准备账号触发 120 次/分钟限制；正常应用配置不变。首次启动需等待两个 `/health` 返回 200。此环境只测试消息，未配置共享附件目录。

持续负载复测可使用 `-pairs 50 -messages 1200 -rate 20 -drain 5s`，对应 100 连接、约 60 秒发送阶段。

阶梯测试可分别将 `-pairs` 设为 10、50、100、250、500，对应 20、100、200、500、1000 个连接。每个连接同时发送和读取；每条消息有唯一 `clientMsgID`，正文携带运行 ID、发送者编号和递增序号。双向发送会同时观察对端推送和自身消息回显。

测试后停止专用环境：

```bash
docker compose -p gochat-bench -f loadtest/compose.yml down
```

测试数据保留在此 Compose 项目的 `bench_pg` 卷中；需要从空库复测时可自行清理该测试卷。

## 指标口径

- `Connections`：测量前成功建立的 WebSocket 数量；配合 `UnexpectedReadErrors` 判断是否保持连接。
- 目标发送速率：连接数 × `Rate`；每个连接按时间计划发送，不等待 ACK。`-spread` 会将连接均匀分布在一个发送间隔内，适合测稳定吞吐；未指定时所有连接同步发送，适合测突发压力。写阻塞或客户端调度过载会降低实际速率，落后的计划会追赶；这不是严格的开放到达模型。没有自动重试、重连或补拉，以保留原始问题。
- `SuccessfulWrites`：客户端 WriteJSON 成功数，不等于服务端接收或落库成功。`WriteErrors` 是写失败次数，失败连接停止继续发送，未发送数为 `Planned - SuccessfulWrites - WriteErrors`。写失败也可能已部分到达服务器。
- `SuccessfulWritesPerSecond`：成功写入数 / 发送阶段耗时，不是数据库吞吐。
- `DeliveriesPerSecond`：唯一对端到达数 /（发送阶段 + drain）的耗时，是包含尾部观察时间的保守到达率。
- `ACKMilliseconds`：客户端开始写消息至收到 ACK 的延迟；`PeerMilliseconds`：开始写消息至对端客户端读到推送的延迟。单位均为 ms，输出样本数、P50/P95/P99/最大值，最近秩百分位算法。计时在同一压测进程内使用单调时钟，含客户端排队/调度开销。
- `MissingACKs`、`MissingPeerDeliveries`、`MissingSelfEchoes`：成功写入消息在观察截止时未得到对应事件的数量；只能说明窗口内未到达，不能直接判定数据库永久丢失。延迟分位数只覆盖到达样本，必须同时看缺失数量。
- `DuplicateACKs`、`DuplicatePushes`：重复事件，推送按每个接收连接分别去重；同一消息投递给双方是预期行为。
- `SenderSequenceRegressions`：同一接收连接中，同一发送者的新消息序号小于此前最大序号的次数；每个用户仅有一个发送连接。
- `ConversationIDRegressions`：同一会话在同一接收连接中新消息 ID 小于此前最大 ID 的次数。它保留为旧协议的诊断项；全局数据库 ID 不能表示会话顺序，因此不作为通过条件。
- `ConversationSeqRegressions`：同一会话在同一接收连接中，服务端 `seq` 小于此前最大 `seq` 的次数。该项覆盖自身回显和对端消息，是有序投递的通过条件；报告保留最多 10 条原始样例。

同一发送者有序不等于会话内多发送者全局按 ID 有序。数据库序列 ID 的分配、事务提交、读取接收者、Redis 发布是不同步骤；全局频道只保证按发布顺序消费，不保证按数据库 ID 发布。前端排序后的结果也不等于原始推送有序。

退出码：0 表示未观察到传输异常、同一发送者乱序或会话 `seq` 乱序，1 表示出现此类异常，2 表示准备/连接/配置/报告写入失败。会话 ID 回退只作诊断；退出 0 表示本次观察窗口内的会话序列有序，不替代断线后的 REST 补拉验证。

## 验证与局限

```bash
go test -race ./loadtest
```

测试覆盖重复事件与顺序统计分离、单发送者顺序与会话 ID 顺序区分、百分位计算。真实端到端结果见 `results/`、[本机进程报告](RESULTS.md)和 [Docker 双实例报告](DOCKER_RESULTS.md)。压测工具在内存保存发送时间、到达标记和延迟样本，内存随消息总数增长。

短时阶梯测试只能报告该环境和负载下的观测值，不能作为最大连接容量、长期稳定性或线上 SLA。正式容量评估还需独立压测机、固定资源配额、长时间稳定负载、重复运行和明确的延迟/错误率验收线；群聊扇出、多设备、重连风暴、附件传输均需独立场景。
