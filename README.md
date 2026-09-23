# Go Chat

基于 Gin 的即时聊天后端，配套一个用于手动验证的静态前端页面。当前能力覆盖用户注册登录、好友关系、私聊/群聊、WebSocket 消息推送、文件上传下载、断点上传、Range 下载、Redis 限流和数据库缓存。

## 架构

- 后端：Gin + GORM + PostgreSQL，REST 接口处理账号、好友、群组、消息列表和文件上传下载。
- 认证：JWT access token + refresh token。refresh token 带 jti，服务端用 Redis allowlist 管理（Redis 为必需依赖）；每次刷新轮换并吊销旧 token，重放返回 401；`/v1/user/logout` 吊销 refresh token。WebSocket 通过 `Sec-WebSocket-Protocol` 的 `bearer.<token>` 条目认证，token 不进 URL。前端会在 access token 过期前主动刷新，接口遇到 401 时也会自动刷新后重试。
- 实时消息：Gorilla WebSocket，默认经 Kafka 异步接收。客户端在 Kafka 已接收后得到 `accepted` ACK，消费者批量落库后再推送带权威会话序号的消息。前端据此展示发送中、排队中、已发送、发送失败和已读状态。Kafka 模式要求客户端提供 `clientMsgID`；服务端用 `(sender_id, client_msg_id)` 唯一索引去重，ACK 丢失重发不会重复落库。前端断线后指数退避自动重连，并用 `afterSeq` 按会话序号增量补拉。将 `kafka.enabled` 显式设为 `false` 时，退回同步落库并返回 `stored` ACK。
- 多实例：Kafka 以 `conversationID` 作为消息 key，同一会话固定进入同一分区并按序消费；消费端在一个事务中批量分配序号和写 PostgreSQL。推送经 `internal/wsbus` 路由，Redis Pub/Sub 会把一个提交批次合成一次发布，各实例再按数组顺序投递本地连接。Kafka 关闭时继续使用 Redis Lua 序号门和数据库发布水位恢复机制。
- 缓存：Redis 为必需依赖；资料通过显式字段映射存 Hash，refresh token 用户 ID 存 String，通过 Lua 原子轮换。启动连接失败会退出；运行时资料缓存失败回源数据库，Token 操作失败返回错误。
- 运维：`GET /health` 返回数据库和 Redis 状态（数据库不可用时返回 503）；收到 SIGINT/SIGTERM 后优雅停机（停止监听、等待存量请求、关闭全部 WebSocket 连接）。
- 文件：默认使用本地存储 `uploads/`；头像可通过 `/uploads/...` 公开访问，聊天附件必须走 `/v1/file/:id/download` 鉴权下载，普通附件支持图片、PDF、Word、TXT 和 ZIP。
- 前端：`web/` 是静态测试页，默认请求 `http://localhost:8080`，页面端口固定为 `5173`。

```mermaid
flowchart LR
    Browser["静态前端 web/"] -->|REST| Gin["Gin API"]
    Browser <-->|WebSocket| WSHub["WebSocket Hub"]
    Gin --> Service["Service 层"]
    WSHub -->|消息校验| Service
    WSHub -->|默认：按 conversationID 入队| Kafka[("Kafka")]
    Kafka --> Consumer["分区消费者：批量落库后发布"]
    Consumer --> Service
    Consumer --> Bus["wsbus"]
    Bus --> Redis
    Service --> Repo["Repository"]
    Repo --> PG[("PostgreSQL")]
    Service --> Redis[("Redis")]
    Service --> Storage["Storage 接口"]
    Storage --> Local[("本地 uploads/")]
    Gin --> Goose["goose migrations"]
    Goose --> PG
    WSHub <-->|"wsbus Pub/Sub 广播（多实例路由）"| Redis
```

## 目录结构

```text
cmd/                  程序入口，负责初始化配置、数据库、Redis、存储和路由
configs/              默认 TOML 配置；本地复制 config.example.toml 为 config.toml，Docker 用 config.docker.toml
internal/auth/        JWT 生成与校验
internal/cache/       Redis 客户端、缓存 Store 和缓存 key 定义
internal/controller/  Gin handler 与 WebSocket handler
internal/dto/         HTTP/WS 入参和出参结构
internal/middleware/  鉴权、CORS、请求日志、限流
internal/messagequeue/ Kafka 消息入口和分区顺序消费
internal/model/       GORM 数据模型
internal/presence/    WebSocket 在线状态存储
internal/ratelimit/   内存/Redis 固定窗口限流
internal/repository/  数据库访问层
internal/router/      路由注册
internal/service/     业务逻辑
internal/storage/     文件存储抽象和本地磁盘实现；后续可扩展 MinIO/OSS
internal/ws/          WebSocket Hub 和连接生命周期
internal/wsbus/       跨实例推送总线：进程内直投 / Redis Pub/Sub 广播
migrations/           goose SQL migrations，服务启动时自动执行
pkg/                  通用错误、日志和响应封装
web/                  静态测试前端
loadtest/             多实例 WebSocket 压测工具、独立 Compose 环境与报告
smoketest/            冒烟测试工具
deploy/               nginx 多实例入口配置
uploads/              本地运行时文件目录，不提交到代码库
logs/                 本地运行时日志目录，不提交到代码库
```

## Docker Compose 启动

```bash
docker compose up --build
```

启动后访问：

- 前端：`http://localhost:5173`
- 后端：`http://localhost:8080`
- PostgreSQL：`localhost:5432`
- Redis：`localhost:6379`

Compose 会启动 PostgreSQL、Redis、Kafka、两个单体后端、nginx 入口和前端。`kafka-init` 会创建 `chat-messages` topic（64 个分区、1 个副本），完成后后端才启动。nginx 在两个后端之间按活跃连接数负载均衡 REST 和 WebSocket；后端通过 Redis Pub/Sub 转发跨实例推送。后端容器会把 `configs/config.docker.toml` 挂载成容器内的 `configs/config.toml`，所以数据库地址使用 `postgres`，Redis 地址使用 `redis:6379`，Kafka 地址使用 `kafka:9092`。Kafka 仅供 Compose 内部访问，未映射宿主机端口。

打开前端后注册两个账号，登录其中一个并通过邮箱添加另一个为好友；切换到第二个账号接受申请，即可在会话中测试聊天。登录页可以修改后端地址。前端功能和操作说明见 [web/README.md](web/README.md)。

停止服务：

```bash
docker compose down
```

如果要连同 PostgreSQL、Redis 和 Kafka 数据卷一起清掉（宿主机的 `uploads/`、`logs/` 目录仍保留）：

```bash
docker compose down -v
```

## 本地启动

需要 Go 1.25.0 或更高版本、PostgreSQL 和 Redis；默认异步模式还需要本机可访问的 Kafka。前端使用 Python 3 提供静态服务，执行 `npm run dev` 还需安装 npm。以下后端命令在仓库根目录执行。

复制示例配置并按本机情况修改：

```bash
cp configs/config.example.toml configs/config.toml
```

至少修改 `[database] password`，并选择消息处理模式：

- 默认异步模式：保留 `[kafka] enabled = true`，将 `brokers` 设置为本机可访问的地址，并提前创建 `topic` 指定的主题（默认 `chat-messages`）。仓库 Compose 的 Kafka 仅公布内部地址 `kafka:9092`，不能直接供宿主机上的后端使用。
- 同步模式：将 `[kafka] enabled = false`，只需准备 PostgreSQL 和 Redis。关闭 Kafka 必须显式配置；省略该字段仍默认启用。

然后启动后端：

```bash
go run ./cmd
```

启动前端：

```bash
cd web
npm run dev
```

也可以在仓库根目录直接运行 `python3 -m http.server 5173 --directory web`。打开 `http://localhost:5173`，后端健康检查地址为 `http://localhost:8080/health`。

## 配置说明

后端从 `configs/config.toml` 读取应用配置；文件不存在时使用代码中的默认值。本地配置文件不提交到代码库，请从 [configs/config.example.toml](configs/config.example.toml) 复制生成。Docker Compose 会把 [configs/config.docker.toml](configs/config.docker.toml) 挂载成容器内的 `configs/config.toml`。当前配置加载器没有启用环境变量覆盖，修改数据库、Redis 或 Kafka 地址请编辑 TOML 文件。

本地常改字段：

- `[database] password`：本机 PostgreSQL 密码
- `[redis] enabled`：必须为 `true`，启动前需要可连接的 Redis
- `[kafka] enabled`：默认 `true`，异步接收聊天消息；Docker Compose 会创建配置中的 topic，本地运行时需自行准备 Kafka 和该 topic。分区数决定可并行处理的会话数；显式设为 `false` 可退回同步落库模式
- `[kafka] brokers` / `topic` / `group_id`：Kafka 地址、消息主题和消费者组；多个后端实例需使用相同主题和消费者组，分担分区消费
- `[kafka] consumer_workers`：每个实例并行处理分区的 worker 数，默认 `4`；同一分区内仍串行处理
- `[database] max_open_conns`：每个后端实例的数据库最大连接数，示例为 `30`
- `[log] path`：日志文件路径
- `[jwt] secret`：部署时必须换成强随机字符串

## 数据库迁移

项目使用 `github.com/pressly/goose/v3` 执行 SQL migration，不再依赖 GORM `AutoMigrate` 创建表结构。入口在 [internal/database/database.go](internal/database/database.go)：

1. `cmd/main.go` 启动时调用 `database.InitDB`
2. `InitDB` 先确保目标 PostgreSQL database 存在
3. 连接目标库后调用 `runMigrations`
4. `runMigrations` 执行 `goose.SetDialect("postgres")` 和 `goose.Up(sqlDB, "migrations")`

空库初始化会随服务启动自动执行。需要手动执行时，可以使用 goose CLI。以下连接串使用 Compose 默认账号；本地运行时请替换为实际数据库账号和密码：

```bash
goose -dir migrations postgres "postgres://postgres:postgres@localhost:5432/chat_proj?sslmode=disable" up
```

回滚最近一次 migration：

```bash
goose -dir migrations postgres "postgres://postgres:postgres@localhost:5432/chat_proj?sslmode=disable" down
```

后续表结构变更不要直接改 `001_init.sql`，应新增递增版本 SQL，当前已有 `001`–`007`，下一次可新增 `008_xxx.sql`。

## 文件消息流程

前端会先对完整文件计算 SHA-256。小文件直接调用 `POST /v1/file/upload`，在 `multipart/form-data` 中携带 `sha256`；大文件使用 `POST /v1/file/upload/init` 创建上传会话，并在 init JSON 中携带完整文件的 `sha256`，再按分片调用 `PUT /v1/file/upload/chunks/:uploadID/:index`，最后调用 `POST /v1/file/upload/complete/:uploadID`。

上传接口只负责把文件存起来并返回文件元信息。真正“发给对方”的动作是前端通过 WebSocket 发送一条普通消息，`content` 内容类似：

```json
{"kind":"file","id":12,"filename":"abc.pdf","url":"/v1/file/12/download","size":1024,"contentType":"application/pdf","sha256":"88d4266fd4e6338d13b845fcf289579d209c897823b9217da3e161936f031589"}
```

接收方看到的是这个消息渲染出来的文件卡片，点击下载时再访问 `/v1/file/12/download`。服务端会根据消息归属的会话校验权限。

```mermaid
sequenceDiagram
    participant A as 发送方前端
    participant API as Gin 文件接口
    participant FS as Storage
    participant WS as WebSocket
    participant Q as Kafka / 消费者
    participant DB as PostgreSQL
    participant B as 接收方前端

    A->>A: 计算完整文件 SHA-256
    A->>API: POST /v1/file/upload 或分片 init 携带 sha256
    API->>FS: 保存文件内容并重新计算 SHA-256
    API->>API: 比对客户端 sha256 和服务端 sha256
    API->>DB: 写入 files 元数据
    API-->>A: 返回 fileID、filename、download URL、sha256
    A->>WS: 发送文件消息 content={kind:file,id,...}
    WS->>Q: 校验后入队
    WS-->>A: message_ack status=accepted
    Q->>DB: 同一事务写入 messages 并绑定 file.conversation_id
    Q-->>A: 经 wsbus 推送已落库消息
    Q-->>B: 经 wsbus 推送文件消息
    B->>API: GET /v1/file/:id/download
    API->>DB: 校验文件所属会话和成员权限
    API->>FS: 流式读取文件
    API-->>B: Content-Disposition + X-Content-SHA256 下载响应
```

## 断点上传流程

```mermaid
sequenceDiagram
    participant C as 前端
    participant API as 文件接口
    participant FS as Storage .parts
    participant DB as PostgreSQL

    C->>API: POST /v1/file/upload/init
    API->>DB: 创建 upload_sessions，保存完整文件 sha256
    API-->>C: uploadID、chunkSize、totalChunks
    C->>API: GET /v1/file/upload/status/:uploadID
    API->>DB: 查询已上传 chunks
    API-->>C: uploadedChunks
    loop 缺失分片
        C->>API: PUT /v1/file/upload/chunks/:uploadID/:index
        API->>FS: 保存临时分片
        API->>DB: upsert upload_chunks
    end
    C->>API: POST /complete/:uploadID
    API->>DB: 校验分片数量和每片 size
    API->>FS: 合并分片为正式文件
    API->>API: 校验合并后文件 SHA-256
    API->>DB: 创建 files，标记 session completed
    API-->>C: 文件元数据
```

文件系统会在普通上传和分片合并时计算完整文件的 SHA-256。客户端上传时如果携带 `sha256`，服务端会和实际落盘内容的 SHA-256 比对，不一致会删除已生成的正式文件并返回失败；一致时写入 `files.sha256`，并在上传响应中返回 `sha256`。下载接口会通过 `X-Content-SHA256` 暴露该值，客户端可在下载后重新计算哈希并比对，用来判断文件内容是否损坏或被篡改。这里解决的是完整性校验；如果要做“文件内容别人看不懂”的能力，还需要在存储层另加加密。

## WebSocket 消息状态流程

默认 Kafka 模式下，`accepted` 仅表示 Kafka 已接收消息，不表示 PostgreSQL 已落库。消息落库后的 `message` 推送携带真实消息 ID 和会话 `seq`，前端收到自身回显后才显示已发送；消费者不会再补发 `stored` ACK。断线重连时，前端用最后连续收到的 `seq` 调用 `POST /v1/message/list`，通过 `afterSeq` 补拉当前会话消息；接口也保留 `afterMessageID` 供旧客户端使用。

```mermaid
sequenceDiagram
    participant A as 发送方前端
    participant WS as WebSocket Handler
    participant K as Kafka
    participant C as 分区消费者
    participant DB as PostgreSQL
    participant Bus as wsbus / Redis
    participant B as 接收方前端
    participant API as REST API

    A->>A: 本地插入消息 status=sending
    A->>WS: type=message, clientMsgID
    WS->>WS: 校验消息与会话权限
    WS->>K: 按 conversationID 入队
    K-->>WS: 接收成功
    WS-->>A: message_ack status=accepted
    A->>A: 显示排队中
    K->>C: 按分区顺序消费
    C->>DB: 事务内批量分配 seq 并写入 messages
    C->>Bus: 发布已提交的消息批次
    Bus-->>A: type=message, id, seq, clientMsgID
    A->>A: status=sent，合并本地临时消息
    Bus-->>B: type=message, id, seq
    C->>K: 提交已处理偏移量
    B->>API: POST /v1/message/read
    API->>DB: 更新 last_read_message_id
    API->>Bus: type=message_read
    Bus-->>A: 已读回执
    A->>A: status=read
```

显式关闭 Kafka 后，WebSocket handler 同步落库，返回 `message_ack`（`status=stored`，包含 `messageID`、`seq`、`createdAt`），前端据此显示已发送。随后通过 Redis Lua 序号门发布消息，数据库发布水位与后台恢复任务用于补发。

前端每次等待 ACK 10 秒，超时后沿用原消息和 `clientMsgID` 自动重试，最多重试 3 次（加上首次发送共 4 次）。最后一次仍超时则标记为 `failed`；收到 ACK（包括 `accepted`）或发送者自己的消息推送后停止重试；收到 `accepted` 后等待落库推送，不再按 ACK 超时重发。服务端返回带 `clientMsgID` 的 `type=error`、发送异常或连接断开时，停止重试并标记失败。

## 测试

```bash
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test ./...
node --check web/app.js
node --test web/*.test.mjs
```

Redis 集成测试需要本机有 Redis，并显式打开：

```bash
CHAT_REDIS_INTEGRATION=1 GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test ./internal/cache ./internal/service ./internal/wsbus -run 'Redis|Cache' -count=1
```

缓存迁移：资料与刷新令牌键使用 `v2:` 前缀，旧 JSON 缓存自然过期；升级后旧刷新令牌失效，需要重新登录。缓存测试使用 miniredis 协议服务执行 RedisStore 与 Lua，不提供生产内存缓存实现。

## 多实例压测

[压测工具与运行方法](loadtest/README.md)支持双向并发私聊，统计 ACK/端到端延迟、窗口内未到达、重复、发送者序号乱序和会话消息 ID 回退。[实测报告](loadtest/RESULTS.md)记录独立双后端环境的阶梯负载结果。

[Docker 双实例 Redis 实测](loadtest/DOCKER_RESULTS.md)：包含 20–1000 连接阶梯负载、两档约 60 秒持续负载及容器 CPU/内存采样。
