// 端到端冒烟：从 edge 单入口（默认 http://localhost:8080）完整走一遍核心链路，
// 覆盖拆分部署的关键路径：edge 分流、gateway WS 接入、gRPC 转发、logic 落库、
// wsbus 跨服务推送、幂等去重、多端同步、增量补拉、token 轮换与吊销。
// 用法：go run ./smoketest [baseURL]
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var base = "http://localhost:8080"

var failures []string

func check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("PASS  %s\n", name)
	} else {
		fmt.Printf("FAIL  %s — %s\n", name, detail)
		failures = append(failures, name)
	}
}

func fatal(name string, err error) {
	fmt.Printf("FATAL %s — %v\n", name, err)
	os.Exit(1)
}

func post(path, token string, body any) (int, map[string]any) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("POST "+path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	return resp.StatusCode, decoded
}

type wsClient struct {
	conn *websocket.Conn
	name string
}

func dialWS(name, token string) *wsClient {
	wsURL := strings.Replace(base, "http", "ws", 1) + "/v1/ws"
	dialer := websocket.Dialer{Subprotocols: []string{"chat", "bearer." + token}}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		fatal("dial ws "+name, err)
	}
	return &wsClient{conn: conn, name: name}
}

// expect 读取消息直到出现指定 type（跳过其他推送），超时报错。
func (c *wsClient) expect(msgType string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		_ = c.conn.SetReadDeadline(deadline)
		var payload map[string]any
		if err := c.conn.ReadJSON(&payload); err != nil {
			return nil, fmt.Errorf("%s waiting for %s: %w", c.name, msgType, err)
		}
		if payload["type"] == msgType {
			return payload, nil
		}
		// 等 ACK/推送时收到业务错误应立即失败，而不是静默丢弃直到超时。
		if payload["type"] == "error" && msgType != "error" {
			return nil, fmt.Errorf("%s got error envelope while waiting for %s: %v", c.name, msgType, payload["data"])
		}
	}
}

// expectSilence 确认窗口内没有指定 type 的消息到达。
func (c *wsClient) expectSilence(msgType string, window time.Duration) error {
	deadline := time.Now().Add(window)
	for {
		_ = c.conn.SetReadDeadline(deadline)
		var payload map[string]any
		if err := c.conn.ReadJSON(&payload); err != nil {
			return nil // 超时 = 静默，符合预期
		}
		if payload["type"] == msgType {
			return fmt.Errorf("%s unexpectedly received %s: %v", c.name, msgType, payload)
		}
	}
}

func data(payload map[string]any) map[string]any {
	d, _ := payload["data"].(map[string]any)
	return d
}

func main() {
	if len(os.Args) > 1 {
		base = os.Args[1]
	}
	suffix := time.Now().UnixNano()
	emailA := fmt.Sprintf("smoke-a-%d@test.com", suffix)
	emailB := fmt.Sprintf("smoke-b-%d@test.com", suffix)

	// 0. 健康检查（edge → logic）
	resp, err := http.Get(base + "/health")
	if err != nil {
		fatal("GET /health", err)
	}
	resp.Body.Close()
	check("health via edge", resp.StatusCode == 200, fmt.Sprintf("status %d", resp.StatusCode))

	// 1. 注册 + 登录
	code, _ := post("/v1/user/register", "", map[string]any{"email": emailA, "password": "pass1234", "nickname": "A"})
	check("register A", code == 200, fmt.Sprintf("status %d", code))
	code, _ = post("/v1/user/register", "", map[string]any{"email": emailB, "password": "pass1234", "nickname": "B"})
	check("register B", code == 200, fmt.Sprintf("status %d", code))

	_, loginA := post("/v1/user/login", "", map[string]any{"email": emailA, "password": "pass1234"})
	_, loginB := post("/v1/user/login", "", map[string]any{"email": emailB, "password": "pass1234"})
	tokenA := data(loginA)["token"].(string)
	refreshA := data(loginA)["refresh_token"].(string)
	tokenB := data(loginB)["token"].(string)
	check("login returns tokens", tokenA != "" && tokenB != "" && refreshA != "", "missing token fields")

	// 2. refresh 轮换 + 旧 token 重放必须 401
	code, refreshed := post("/v1/user/refresh", "", map[string]any{"refreshToken": refreshA})
	check("refresh rotates", code == 200 && data(refreshed)["refresh_token"] != "", fmt.Sprintf("status %d", code))
	code, _ = post("/v1/user/refresh", "", map[string]any{"refreshToken": refreshA})
	check("replayed refresh token rejected", code == 401, fmt.Sprintf("status %d, want 401", code))
	newRefreshA := data(refreshed)["refresh_token"].(string)
	tokenA = data(refreshed)["token"].(string)

	// 3. 建立好友关系（消息前置条件）
	code, _ = post("/v1/friend/add", tokenA, map[string]any{"friendEmail": emailB})
	check("A adds B", code == 200, fmt.Sprintf("status %d", code))
	_, pending := post("/v1/friend/pending", tokenB, map[string]any{})
	pendingList, _ := pending["data"].([]any)
	if len(pendingList) == 0 {
		fatal("pending list empty", fmt.Errorf("no friend request delivered"))
	}
	reqID := pendingList[0].(map[string]any)["requestID"].(float64)
	code, _ = post("/v1/friend/accept", tokenB, map[string]any{"requestID": reqID})
	check("B accepts", code == 200, fmt.Sprintf("status %d", code))

	var idA, idB float64
	_, friendsOfB := post("/v1/friend/list", tokenB, map[string]any{})
	for _, f := range friendsOfB["data"].([]any) {
		idA = f.(map[string]any)["userID"].(float64)
	}
	_, friendsOfA := post("/v1/friend/list", tokenA, map[string]any{})
	for _, f := range friendsOfA["data"].([]any) {
		idB = f.(map[string]any)["userID"].(float64)
	}
	check("friend ids resolved", idA > 0 && idB > 0, fmt.Sprintf("idA=%v idB=%v", idA, idB))

	// 4. WS：错误 token 必须被拒
	wsURL := strings.Replace(base, "http", "ws", 1) + "/v1/ws"
	badDialer := websocket.Dialer{Subprotocols: []string{"chat", "bearer.invalid-token"}}
	_, badResp, badErr := badDialer.Dial(wsURL, nil)
	badStatus := 0
	if badResp != nil {
		badStatus = badResp.StatusCode
	}
	check("ws rejects bad token", badErr != nil && badStatus == 401, fmt.Sprintf("err=%v status=%d", badErr, badStatus))

	// 5. WS 三连接：A 两个设备 + B 一个（A2 验证发送方多端同步）
	wsA1 := dialWS("A1", tokenA)
	wsA2 := dialWS("A2", tokenA)
	wsB := dialWS("B", tokenB)
	defer wsA1.conn.Close()
	defer wsA2.conn.Close()
	defer wsB.conn.Close()

	// 6. A1 发消息：A1 收 ACK，B 收带会话目标的推送，A2 收多端同步推送
	clientMsgID := fmt.Sprintf("smoke-%d", suffix)
	sendPayload := map[string]any{
		"type": "message", "clientMsgID": clientMsgID,
		"targetType": "private", "targetID": idB, "content": "hello from A1",
	}
	if err := wsA1.conn.WriteJSON(sendPayload); err != nil {
		fatal("A1 send", err)
	}

	ack, err := wsA1.expect("message_ack", 5*time.Second)
	if err != nil {
		fatal("A1 ack", err)
	}
	msgID := data(ack)["messageID"].(float64)
	check("A1 gets ACK", data(ack)["clientMsgID"] == clientMsgID && msgID > 0, fmt.Sprintf("ack=%v", ack))

	bMsg, err := wsB.expect("message", 5*time.Second)
	if err != nil {
		fatal("B push", err)
	}
	bData := data(bMsg)
	check("B receives push with receiver-view target",
		bData["content"] == "hello from A1" && bData["targetType"] == "private" && bData["targetID"] == idA,
		fmt.Sprintf("data=%v", bData))

	a2Msg, err := wsA2.expect("message", 5*time.Second)
	if err != nil {
		fatal("A2 multi-device push", err)
	}
	a2Data := data(a2Msg)
	check("A2 multi-device sync with clientMsgID",
		a2Data["clientMsgID"] == clientMsgID && a2Data["targetID"] == idB,
		fmt.Sprintf("data=%v", a2Data))

	// 7. 幂等：重发同 clientMsgID → 只有 ACK（同 messageID），B 不应再收推送
	if err := wsA1.conn.WriteJSON(sendPayload); err != nil {
		fatal("A1 resend", err)
	}
	ack2, err := wsA1.expect("message_ack", 5*time.Second)
	if err != nil {
		fatal("A1 duplicate ack", err)
	}
	check("duplicate resend returns same messageID", data(ack2)["messageID"] == msgID,
		fmt.Sprintf("first=%v second=%v", msgID, data(ack2)["messageID"]))
	err = wsB.expectSilence("message", 2*time.Second)
	check("B gets no duplicate push", err == nil, fmt.Sprintf("%v", err))

	// 8. 增量补拉：afterMessageID
	code, incr := post("/v1/message/list", tokenB, map[string]any{
		"targetType": "private", "targetID": idA, "afterMessageID": msgID - 1,
	})
	incrList, _ := incr["data"].([]any)
	check("incremental pull after cursor", code == 200 && len(incrList) == 1, fmt.Sprintf("status=%d n=%d", code, len(incrList)))

	// 9. 已读回执：B 标记已读 → A1 收 message_read
	code, _ = post("/v1/message/read", tokenB, map[string]any{
		"targetType": "private", "targetID": idA, "messageID": msgID,
	})
	check("B marks read", code == 200, fmt.Sprintf("status %d", code))
	readEvt, err := wsA1.expect("message_read", 5*time.Second)
	if err != nil {
		fatal("A1 read receipt", err)
	}
	check("A1 gets read receipt", data(readEvt)["messageID"] == msgID, fmt.Sprintf("evt=%v", readEvt))

	// 10. 会话列表
	code, sessions := post("/v1/message/sessions", tokenB, map[string]any{})
	sessionList, _ := sessions["data"].([]any)
	check("session list", code == 200 && len(sessionList) >= 1, fmt.Sprintf("status=%d n=%d", code, len(sessionList)))

	// 11. logout 吊销：新 refresh token 登出后重放必须 401
	code, _ = post("/v1/user/logout", "", map[string]any{"refreshToken": newRefreshA})
	check("logout", code == 200, fmt.Sprintf("status %d", code))
	code, _ = post("/v1/user/refresh", "", map[string]any{"refreshToken": newRefreshA})
	check("revoked refresh token rejected", code == 401, fmt.Sprintf("status %d, want 401", code))

	fmt.Println()
	if len(failures) > 0 {
		fmt.Printf("RESULT: %d FAILED — %s\n", len(failures), strings.Join(failures, ", "))
		os.Exit(1)
	}
	fmt.Println("RESULT: ALL PASS")
}
