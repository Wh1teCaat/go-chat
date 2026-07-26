# API 文档

所有普通接口默认返回统一 JSON：

```json
{
  "code": 0,
  "message": "ok",
  "data": {},
  "request_id": "optional-request-id"
}
```

除注册、登录、刷新 token、登出、健康检查和公开头像访问外，接口都需要请求头：

```http
Authorization: Bearer <token>
```

浏览器 WebSocket API 无法自定义请求头，token 通过 `Sec-WebSocket-Protocol` 子协议条目传递（不再支持查询参数传 token，避免 token 进入访问日志）：

```js
new WebSocket("ws://localhost:8080/v1/ws", ["chat", "bearer." + token]);
```

服务端固定选择 `chat` 作为协商结果，`bearer.<token>` 条目只用于认证。

### 健康检查

`GET /health`（无需认证）

```json
{ "status": "ok", "db": "ok", "redis": "ok" }
```

数据库不可用时返回 503；Redis 是可降级依赖，只如实上报 `ok` / `down` / `disabled`，不影响整体健康判断。

## 用户

### 注册

`POST /v1/user/register`

```json
{
  "email": "alice@example.com",
  "password": "123456",
  "nickname": "alice",
  "avatar": "/uploads/2026/06/15/avatar.png"
}
```

### 登录

`POST /v1/user/login`

```json
{
  "email": "alice@example.com",
  "password": "123456"
}
```

返回：

```json
{
  "token": "jwt-token",
  "expire_at": 1782285600,
  "refresh_token": "refresh-jwt-token",
  "refresh_expire_at": 1782886800
}
```

### 刷新 Token

`POST /v1/user/refresh`

```json
{
  "refreshToken": "refresh-jwt-token"
}
```

返回新的 access token 和 refresh token：

```json
{
  "token": "new-jwt-token",
  "expire_at": 1782286500,
  "refresh_token": "new-refresh-jwt-token",
  "refresh_expire_at": 1782887700
}
```

refresh token 采用轮换机制：每次刷新会吊销旧 refresh token 并签发新的。旧 token 再次使用（重放）会返回 401，客户端必须保存响应里的新 `refresh_token`。服务端用 Redis 保存有效 refresh token 的 allowlist；未启用 Redis 时退回内存存储，服务重启后需要重新登录。

### 登出

`POST /v1/user/logout`（无需 access token，过期后也能登出）

```json
{
  "refreshToken": "refresh-jwt-token"
}
```

吊销服务端保存的 refresh token。access token 本身短期有效、无状态，过期后没有可用的 refresh token 即等于完全登出。token 无效或已吊销时同样返回成功（幂等）。

### 修改资料

`POST /v1/user/update`

```json
{
  "nickname": "new-name",
  "avatar": "/uploads/2026/06/15/avatar.png"
}
```

头像文件先用 `purpose=avatar` 调上传接口，成功后把返回的 `url` 写入 `avatar`。

## 好友

### 发送好友申请

`POST /v1/friend/add`

```json
{
  "friendEmail": "bob@example.com"
}
```

### 处理好友申请

`POST /v1/friend/accept`

```json
{
  "requestID": 1
}
```

`POST /v1/friend/reject`

```json
{
  "requestID": 1
}
```

### 删除好友

`POST /v1/friend/remove`

```json
{
  "friendID": 2
}
```

### 查询好友和待处理申请

- `GET /v1/friend/list`
- `GET /v1/friend/pending`

好友列表的单项结构：

```json
{
  "userID": 2,
  "nickname": "bob",
  "avatar": "/uploads/2026/06/15/avatar.png",
  "status": "accepted",
  "online": true
}
```

`online` 来自 WebSocket 连接状态。Redis 开启时在线状态写 Redis；Redis 未开启时使用进程内内存状态。

## 群组

### 创建和修改群

`POST /v1/group/create`

```json
{
  "name": "backend"
}
```

`POST /v1/group/update`

```json
{
  "groupID": 1,
  "name": "new-backend"
}
```

### 查询群

- `GET /v1/group/mine`：我创建的群
- `GET /v1/group/joined`：我加入的群

### 入群审批

`POST /v1/group/join`

```json
{
  "groupID": 1
}
```

`POST /v1/group/join/review`

```json
{
  "requestID": 1,
  "status": "approved"
}
```

`status` 可取 `approved` 或 `rejected`。

查询：

- `POST /v1/group/join-requests`：某个群的入群申请
- `GET /v1/group/join-requests/mine`：我提交的入群申请
- `GET /v1/group/join-requests/reviewable`：我能审批的申请

### 群成员管理

- `POST /v1/group/invite`
- `POST /v1/group/leave`
- `POST /v1/group/member/remove`
- `POST /v1/group/member/role`
- `POST /v1/group/members`
- `POST /v1/group/transfer-owner`

常用成员操作入参：

```json
{
  "groupID": 1,
  "userID": 2
}
```

角色修改：

```json
{
  "groupID": 1,
  "userID": 2,
  "role": 1
}
```

`role`：`0` 普通成员，`1` 管理员，`2` 群主。

## 消息

### WebSocket 连接

`GET /v1/ws`（token 通过 `Sec-WebSocket-Protocol` 传递，见文档开头）

客户端发送文本消息：

```json
{
  "type": "message",
  "clientMsgID": "client-generated-id",
  "targetType": "private",
  "targetID": 2,
  "content": "hello"
}
```

`clientMsgID` 参与服务端幂等去重：同一发送者的同一 `clientMsgID` 只会落库一次。ACK 丢失后客户端重发同一条消息，服务端会返回原消息的 ACK，不会重复入库，也不会再次推送给接收方。

`targetType` 可取：

- `private`：`targetID` 是好友用户 ID
- `group`：`targetID` 是群 ID

服务端 ACK：

```json
{
  "type": "message_ack",
  "data": {
    "clientMsgID": "client-generated-id",
    "messageID": 100,
    "createdAt": "2026-06-23T10:00:00+08:00"
  }
}
```

前端收到 ACK 后，应使用 `clientMsgID` 找到本地“发送中”消息，把本地临时 ID 替换为服务端 `messageID`，并把状态更新为“已发送”。

接收方收到消息：

```json
{
  "type": "message",
  "data": {
    "id": 100,
    "senderID": 1,
    "content": "hello",
    "createdAt": "2026-06-23T10:00:00+08:00",
    "targetType": "private",
    "targetID": 1
  }
}
```

`targetType`/`targetID` 是接收端视角的会话目标：群聊是群 ID；私聊时接收方看到的 `targetID` 是发送者用户 ID。客户端据此把消息归档到正确的会话。

消息本体也会推送给发送者的全部连接（多标签页/多设备同步），并额外带 `clientMsgID`。发起发送的那个连接会同时收到 ACK 和这条推送，客户端按消息 `id` / `clientMsgID` 去重即可。

服务端处理发送失败时会返回 `error` envelope；如果请求里带了 `clientMsgID`，错误也会原样带回，前端可以把对应本地消息标记为“发送失败”：

```json
{
  "type": "error",
  "data": {
    "status": 403,
    "code": "permission_denied",
    "message": "permission denied",
    "clientMsgID": "client-generated-id"
  }
}
```

消息已读事件：

```json
{
  "type": "message_read",
  "data": {
    "targetType": "private",
    "targetID": 2,
    "messageID": 100,
    "readerID": 2
  }
}
```

私聊里，接收方收到的 `targetID` 是读者用户 ID，方便和当前私聊对象匹配。群聊里，`targetID` 是群 ID。

文件消息的 `content` 是 JSON 字符串：

```json
{"kind":"file","id":12,"filename":"abc.pdf","url":"/v1/file/12/download","size":1024,"contentType":"application/pdf","sha256":"88d4266fd4e6338d13b845fcf289579d209c897823b9217da3e161936f031589"}
```

### 消息列表和会话

`POST /v1/message/list`

```json
{
  "targetType": "private",
  "targetID": 2,
  "beforeMessageID": 100,
  "limit": 20
}
```

- `beforeMessageID`：向上翻历史的游标，按 id 倒序返回更早的消息；为空时返回最新一页。
- `afterMessageID`：断线重连后的增量补拉游标，按 id 升序返回比它更新的消息；与 `beforeMessageID` 互斥，同时传时以 `afterMessageID` 为准。

`POST /v1/message/read`

```json
{
  "targetType": "private",
  "targetID": 2,
  "messageID": 100
}
```

`POST /v1/message/sessions`

## 文件

### 普通上传

`POST /v1/file/upload`

`multipart/form-data` 字段：

- `file`：文件内容
- `purpose`：`avatar`、`chat_image`、`chat_file`
- `sha256`：客户端对完整文件计算出的 SHA-256。传入后，服务端会和实际落盘内容重新计算出的 SHA-256 比对，不一致则上传失败。

普通聊天附件当前允许：图片、PDF、Word 文档（`.doc`、`.docx`）、TXT、ZIP。

返回：

```json
{
  "id": 12,
  "url": "/v1/file/12/download",
  "filename": "abc.pdf",
  "size": 1024,
  "contentType": "application/pdf",
  "sha256": "88d4266fd4e6338d13b845fcf289579d209c897823b9217da3e161936f031589",
  "purpose": "chat_file"
}
```

头像上传返回的是公开 `/uploads/...` 地址；聊天附件返回鉴权下载地址。

### 分片上传

创建上传会话：

`POST /v1/file/upload/init`

```json
{
  "filename": "large.zip",
  "size": 104857600,
  "contentType": "application/zip",
  "purpose": "chat_file",
  "chunkSize": 2097152,
  "sha256": "88d4266fd4e6338d13b845fcf289579d209c897823b9217da3e161936f031589"
}
```

`sha256` 是完整文件的哈希，不是单个分片的哈希。服务端会在所有分片合并后统一比对。

上传分片：

`POST /v1/file/upload/chunks/:uploadID/:index`

`multipart/form-data` 字段：

- `chunk`：当前分片内容

查询状态：

`GET /v1/file/upload/status/:uploadID`

完成上传：

`POST /v1/file/upload/complete/:uploadID`

取消上传：

`POST /v1/file/upload/cancel/:uploadID`

### 下载

`GET /v1/file/:id/download`

支持 `Range` 请求头：

```http
Range: bytes=0-1023
```

下载接口会返回 `Content-Disposition`，浏览器保存时使用用户上传时的原始文件名。
如果数据库中已有文件哈希，还会返回 `X-Content-SHA256`，用于客户端下载后比对文件完整性。

### 公开头像

`GET /uploads/*filepath`

这个路由只允许访问 `purpose=avatar` 的文件，聊天附件不会通过这个路由公开。
