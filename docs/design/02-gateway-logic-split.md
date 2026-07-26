# 阶段 2：拆分 gateway / chat-logic

## 动因

阶段 1 让推送可以跨实例路由，但接入和业务仍在一个进程里，两者的扩缩容维度不同：

- **接入层（WebSocket）**：瓶颈是并发连接数（fd、内存、心跳开销），随在线用户数扩容；
- **业务层（REST + 消息处理）**：瓶颈是 QPS 和数据库压力，随请求量扩容。

绑在一个进程里意味着：为了扛住连接数扩容，就要连带复制整套业务代码和数据库连接池；业务发版重启，还会把所有长连接一起踢掉。拆开后 gateway 几乎不用发版，业务迭代不再断连接。

## 拆分边界

```text
Browser ──REST──▶ edge(nginx) ──▶ chat-logic ─┬─▶ PostgreSQL
   │                 │                        ├─▶ Redis（缓存/限流/token）
   └──WS──▶ edge ──▶ gateway ──gRPC──▶ logic  │
                        ▲                     │
                        └── Redis Pub/Sub ◀───┘（wsbus 推送广播）
```

- **gateway**（`cmd/gateway`，无状态）：JWT 认证、WebSocket 生命周期、心跳、在线状态写入；来消息调 `ChatService.SendMessage`（gRPC）转给 logic；订阅 wsbus 把推送投给本地连接；本地回 ACK/error。
- **chat-logic**（`cmd/logic`）：全部 REST 接口 + gRPC 服务端；消息校验、幂等、落库、推送编排；只发布 wsbus 不订阅（自己没有连接）。
- **edge**（nginx）：对外单一地址，`/v1/ws` 分流到 gateway，其余到 logic。前端完全不感知拆分。
- **单体入口保留**（`cmd/`）：本地开发无 Redis 时仍可单二进制跑通全部功能，三种形态共用同一个镜像。

## RPC 面设计

只有一个方法 `SendMessage`。刻意保持最小：

- 已读回执、好友/群操作走 REST 直达 logic，不经过 gateway，天然不需要 RPC。
- 请求带 `sender_id`——由 gateway 从已验证 JWT 中取出，logic 信任该值。这要求 gRPC 端口只在内网暴露（生产应加 mTLS，见遗留）。
- 响应只含回 ACK 所需字段（messageID/createdAt/duplicate）。消息本体不走 RPC 响应，仍走总线广播——保证"发起连接在哪个 gateway"和"接收方在哪个 gateway"两个问题解耦。
- 错误映射对称：logic 把 apperrors 按 HTTP 语义映射成 gRPC status（400→InvalidArgument 等），gateway 反向还原成 WS error envelope，前端行为与单体完全一致。logic 不可用时 gateway 返回 `logic_unavailable`，前端把消息标记为发送失败，用户可重试（幂等由 clientMsgID 保证，重试不会重复落库）。

## 关键决策

**ACK 为什么不走总线？** ACK 只对发起发送的那个连接有意义，而该连接必然挂在处理这次 RPC 调用的 gateway 上。gRPC 响应返回后本地直投，链路最短；发送者其他设备上的展示由总线的消息推送覆盖。

**gateway 停机为什么可以直接踢连接？** 客户端有指数退避自动重连 + `afterMessageID` 增量补拉（阶段 0）。edge 把新连接分给存活实例，掉线窗口内的消息在重连后补齐。这就是"接入层无状态"的含义：连接可以死，会话状态在数据库和客户端手里。

**为什么还没上服务发现（etcd/consul）？** 当前 gateway→logic 是 nginx/DNS 静态寻址，实例数少时够用。等 logic 再往下拆（message/push 独立服务）、实例动态伸缩时，静态配置维护不动了，那才是引入注册中心的时机——按需引入，不是拆完立刻堆满组件。

## 遗留 / 下一步

- gRPC 明文传输，生产需要 mTLS 或至少网络隔离。
- 文件仍在 logic 本地磁盘，多 logic 实例前必须先换 MinIO/OSS。
- 消息"落库"和"推送"仍在同一次 RPC 里同步完成；引入 Kafka 后可改为落库即返回、推送异步消费，顺带获得离线推送和消息轨迹能力。
- edge 对 gateway 的负载均衡是 nginx DNS 轮询，`--scale gateway=N` 后需重启 edge 生效。
