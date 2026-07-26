# Chat Proj

基于 Gin 的即时聊天后端，配套一个用于手动验证的静态前端页面。当前能力覆盖用户注册登录、好友关系、私聊/群聊、WebSocket 消息推送、文件上传下载、断点上传、Range 下载、Redis 限流和数据库缓存。

## 架构

- 后端：Gin + GORM + PostgreSQL，REST 接口处理账号、好友、群组、消息列表和文件上传下载。
- 认证：JWT access token + refresh token。refresh token 带 jti，服务端用 Redis allowlist 管理（无 Redis 时退回内存）；每次刷新轮换并吊销旧 token，重放返回 401；`/v1/user/logout` 吊销 refresh token。WebSocket 通过 `Sec-WebSocket-Protocol` 的 `bearer.<token>` 条目认证，token 不进 URL。前端会在 access token 过期前主动刷新，接口遇到 401 时也会自动刷新后重试。
- 实时消息：Gorilla WebSocket。客户端发消息后服务端落库并返回 `message_ack`，前端据此展示发送中/已发送/发送失败/已读状态。`clientMsgID` 参与服务端幂等去重（`(sender_id, client_msg_id)` 唯一索引），ACK 丢失重发不会重复落库。消息本体推送给接收方和发送者的全部连接（多标签页/多设备同步），推送带接收端视角的 `targetType`/`targetID`。前端断线后指数退避自动重连，重连成功用 `afterMessageID` 增量补拉断线期间的消息。
- 多实例：推送经 `internal/wsbus` 总线路由——启用 Redis 时走 Pub/Sub 全局频道广播，每个实例只投递本地在线用户，支持多实例水平扩展；无 Redis 时退化为进程内直投。ACK/错误只对发起连接有意义，始终本地直投。设计取舍见 [docs/design/01-multi-instance-ws.md](docs/design/01-multi-instance-ws.md)。
- 服务拆分：支持三种部署形态，共用同一镜像——单体（`cmd/`，本地开发可无 Redis）；拆分部署 `chat-gateway`（WS 接入层，无状态、按连接数扩容）+ `chat-logic`（REST + 业务，按 QPS 扩容），两者以 gRPC 通信（`api/proto/chat/v1`），入口由 nginx edge 按路径分流，前端不感知拆分。拆分边界与 RPC 面设计见 [docs/design/02-gateway-logic-split.md](docs/design/02-gateway-logic-split.md)。
- 缓存：Redis 可选开启；当前用于限流计数、用户资料缓存、群资料缓存、在线状态和 refresh token allowlist，Redis 不可用时退回内存实现。
- 运维：`GET /health` 健康检查（数据库不可用返回 503）；收到 SIGINT/SIGTERM 后优雅停机（停止监听、等待存量请求、关闭全部 WebSocket 连接）。
- 文件：默认使用本地存储 `uploads/`；头像可通过 `/uploads/...` 公开访问，聊天附件必须走 `/v1/file/:id/download` 鉴权下载，普通附件支持图片、PDF、Word、TXT 和 ZIP。
- 前端：`web/` 是静态测试页，默认请求 `http://localhost:8080`，页面端口固定为 `5173`。

拆分部署形态（docker compose 默认）：

```mermaid
flowchart LR
    Browser["静态前端 web/"] -->|"REST + WS（单一地址）"| Edge["edge (nginx)"]
    Edge -->|REST| Logic["chat-logic<br/>Gin + Service + Repo"]
    Edge <-->|WebSocket| GW["chat-gateway<br/>Hub + 认证 + ACK"]
    GW -->|gRPC SendMessage| Logic
    Logic --> PG[("PostgreSQL")]
    Logic -->|"缓存/限流/token"| Redis[("Redis")]
    Logic -->|"wsbus 发布"| Redis
    Redis -->|"wsbus 订阅投递"| GW
    Logic --> Storage["Storage 接口"] --> Local[("本地 uploads/")]
    Logic --> Goose["goose migrations"] --> PG
```

单体形态（`cmd/`）：同一套 internal 代码在一个进程内直连，WS 推送退化为进程内直投。

## 目录结构

```text
api/proto/            gRPC proto 定义（gateway ↔ chat-logic）
cmd/                  单体入口：一个进程承载全部能力，本地开发默认形态
cmd/gateway/          拆分部署：WebSocket 接入层（认证、连接、ACK、总线订阅投递）
cmd/logic/            拆分部署：业务层（REST + gRPC + 落库/推送编排，总线发布）
deploy/               入口代理等部署配置
configs/              默认 TOML 配置；本地复制 config.example.toml 为 config.toml，Docker 用 config.docker.toml
internal/auth/        JWT 生成与校验
internal/cache/       Redis 客户端、缓存 Store 和缓存 key 定义
internal/controller/  Gin handler 与 WebSocket handler
internal/dto/         HTTP/WS 入参和出参结构
internal/middleware/  鉴权、CORS、请求日志、限流
internal/model/       GORM 数据模型
internal/ratelimit/   内存/Redis 固定窗口限流
internal/repository/  数据库访问层
internal/router/      路由注册
internal/service/     业务逻辑
internal/storage/     文件存储抽象和本地磁盘实现；后续可扩展 MinIO/OSS
internal/gateway/     gateway 服务的 WS 处理器（gRPC 转发 + 本地 ACK/error）
internal/rpc/chatpb/  protoc 生成的 gRPC 代码
internal/ws/          WebSocket Hub 和连接生命周期
internal/wsbus/       跨实例推送总线：进程内直投 / Redis Pub/Sub 广播
migrations/           goose SQL migrations，服务启动时自动执行
pkg/                  通用错误、日志和响应封装
web/                  静态测试前端
uploads/              本地运行时文件目录，不提交到代码库
logs/                 本地运行时日志目录，不提交到代码库
```

## Docker Compose 启动

```bash
docker compose up --build
```

启动后访问：

- 前端：`http://localhost:5173`
- 后端入口（edge 代理）：`http://localhost:8080`
- PostgreSQL：`localhost:5432`
- Redis：`localhost:6379`

Compose 以**拆分形态**启动：PostgreSQL、Redis、`chat-logic`（REST + gRPC）、`chat-gateway`（WS 接入）、`edge`（nginx 入口代理，`/v1/ws` 分流到 gateway、其余到 logic）和前端。前端仍然只面对 `localhost:8080` 一个地址。gateway 可水平扩容：

```bash
docker compose up -d --scale gateway=2 && docker compose restart edge
```

后端容器会把 `configs/config.docker.toml` 挂载成容器内的 `configs/config.toml`，所以数据库地址使用 `postgres`，Redis 地址使用 `redis:6379`，gateway 通过 `logic:9090` 连 logic 的 gRPC。

停止服务：

```bash
docker compose down
```

如果要连同数据库和 Redis 数据一起清掉：

```bash
docker compose down -v
```

## 本地启动

先准备 PostgreSQL 和 Redis，复制示例配置并按本机情况修改：

```bash
cp configs/config.example.toml configs/config.toml
```

然后启动后端（单体形态，功能完整，无 Redis 也能跑）：

```bash
go run ./cmd
```

或以拆分形态启动（需要 Redis，先起 logic 再起 gateway；WS 需连 `:8081`，可用本地 nginx 或改前端 WS 地址）：

```bash
go run ./cmd/logic     # REST :8080 + gRPC :9090
go run ./cmd/gateway   # WS :8081
```

启动前端：

```bash
cd web
npm run dev
```

## 配置说明

后端只从 `configs/config.toml` 读取应用配置。本地配置文件不提交到代码库，请从 [configs/config.example.toml](configs/config.example.toml) 复制生成。Docker Compose 会把 [configs/config.docker.toml](configs/config.docker.toml) 挂载成容器内的 `configs/config.toml`，所以 Compose 不需要再通过 `CHAT_DATABASE_HOST`、`CHAT_REDIS_ADDR` 这类环境变量覆盖配置。

本地常改字段：

- `[database] password`：本机 PostgreSQL 密码
- `[redis] enabled`：没有 Redis 时可改成 `false`
- `[log] path`：日志文件路径
- `[jwt] secret`：部署时必须换成强随机字符串

## 数据库迁移

项目使用 `github.com/pressly/goose/v3` 执行 SQL migration，不再依赖 GORM `AutoMigrate` 创建表结构。入口在 [internal/database/database.go](internal/database/database.go)：

1. `cmd/main.go` 启动时调用 `database.InitDB`
2. `InitDB` 先确保目标 PostgreSQL database 存在
3. 连接目标库后调用 `runMigrations`
4. `runMigrations` 执行 `goose.SetDialect("postgres")` 和 `goose.Up(sqlDB, "migrations")`

空库初始化会随服务启动自动执行。需要手动执行时，可以使用 goose CLI：

```bash
goose -dir migrations postgres "postgres://postgres:postgres@localhost:5432/chat_proj?sslmode=disable" up
```

回滚最近一次 migration：

```bash
goose -dir migrations postgres "postgres://postgres:postgres@localhost:5432/chat_proj?sslmode=disable" down
```

后续表结构变更不要直接改 `001_init.sql`，应新增递增版本 SQL，例如 `004_xxx.sql`。

## 文件消息流程

前端会先对完整文件计算 SHA-256。小文件直接调用 `POST /v1/file/upload`，在 `multipart/form-data` 中携带 `sha256`；大文件使用 `POST /v1/file/upload/init` 创建上传会话，并在 init JSON 中携带完整文件的 `sha256`，再按分片调用 `POST /v1/file/upload/chunks/:uploadID/:index`，最后调用 `POST /v1/file/upload/complete/:uploadID`。

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
    participant DB as PostgreSQL
    participant B as 接收方前端

    A->>A: 计算完整文件 SHA-256
    A->>API: POST /v1/file/upload 或分片 init 携带 sha256
    API->>FS: 保存文件内容并重新计算 SHA-256
    API->>API: 比对客户端 sha256 和服务端 sha256
    API->>DB: 写入 files 元数据
    API-->>A: 返回 fileID、filename、download URL、sha256
    A->>WS: 发送文件消息 content={kind:file,id,...}
    WS->>DB: 写入 messages，并绑定 file.conversation_id
    WS-->>A: message_ack
    WS-->>B: 推送文件消息
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
        C->>API: POST /chunks/:uploadID/:index
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

```mermaid
sequenceDiagram
    participant A as 发送方前端
    participant WS as WebSocket Handler
    participant DB as PostgreSQL
    participant B as 接收方前端

    A->>A: 本地插入消息 status=sending
    A->>WS: type=message, clientMsgID
    WS->>DB: 创建 messages
    WS-->>A: type=message_ack, clientMsgID, messageID
    A->>A: status=sent，临时 ID 替换为 messageID
    WS-->>B: type=message
    B->>WS: POST /v1/message/read
    WS->>DB: 更新 last_read_message_id
    WS-->>A: type=message_read
    A->>A: status=read
```

如果服务端返回 `type=error` 且带 `clientMsgID`，或前端等待 ACK 超时，前端会把对应本地消息标记为 `failed`。

## 测试

```bash
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test ./...
node --check web/app.js
node --test web/app-helpers.test.mjs
```

Redis 集成测试需要本机有 Redis，并显式打开：

```bash
CHAT_REDIS_INTEGRATION=1 GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test ./internal/cache ./internal/service ./internal/wsbus -run 'Redis|Cache' -count=1
```
