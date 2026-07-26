// 压测器：对拆分部署的完整栈（经 edge 单入口）施加真实聊天负载，产出量化指标。
//
// 负载模型：N 个用户两两结为好友对，每用户开 M 个 WebSocket 连接（多设备），
// 每对好友的 0 号设备互发消息。一条消息的投递扇出 = 对端 M 个设备 + 自己另外 M-1 个
// 设备（多端同步），压的是「gRPC 转发 → 落库 → Kafka → 消费组 → Hub 投递」全链路。
//
// 指标：连接建立成功率与耗时、ACK 延迟（发送→落库确认）、端到端投递延迟
// （发送→任一在线设备收到推送）的 P50/P95/P99、投递吞吐、错误计数。
//
// 用法：go run ./benchtest -users 200 -devices 5 -rate 2 -duration 30s
// 注意：连接数多时先 ulimit -n 65536；用户首次创建后可重复复用（邮箱确定性生成）。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var (
	base     = flag.String("base", "http://localhost:8080", "edge 入口地址")
	users    = flag.Int("users", 200, "用户数（必须为偶数，两两结对）")
	devices  = flag.Int("devices", 5, "每用户 WebSocket 连接数（多设备）")
	rate     = flag.Float64("rate", 2, "每个好友对每秒互发消息数")
	duration = flag.Duration("duration", 30*time.Second, "发压时长")
	dialRate = flag.Int("dial-rate", 200, "每秒建立的 WS 连接数（避免惊群）")
)

type metrics struct {
	mu          sync.Mutex
	ackLatency  []time.Duration
	e2eLatency  []time.Duration
	sent        atomic.Int64
	acked       atomic.Int64
	delivered   atomic.Int64
	sendErrs    atomic.Int64
	readErrs    atomic.Int64
	connFail    atomic.Int64
	connOK      atomic.Int64
	connSetupNs atomic.Int64
}

var m metrics

func (m *metrics) addAck(d time.Duration) {
	m.acked.Add(1)
	m.mu.Lock()
	m.ackLatency = append(m.ackLatency, d)
	m.mu.Unlock()
}

func (m *metrics) addE2E(d time.Duration) {
	m.delivered.Add(1)
	m.mu.Lock()
	m.e2eLatency = append(m.e2eLatency, d)
	m.mu.Unlock()
}

func percentiles(samples []time.Duration) (p50, p95, p99, max time.Duration) {
	if len(samples) == 0 {
		return
	}
	sorted := append([]time.Duration(nil), samples...)
	slices.Sort(sorted)
	at := func(q float64) time.Duration {
		idx := int(q * float64(len(sorted)-1))
		return sorted[idx]
	}
	return at(0.50), at(0.95), at(0.99), sorted[len(sorted)-1]
}

// ====== REST 辅助 ======

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 256,
	},
}

func post(path, token string, body any) (int, map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, *base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	return resp.StatusCode, decoded, nil
}

// postRetry429 命中限流时按 Retry-After 退避重试。
func postRetry429(path, token string, body any) (map[string]any, error) {
	for {
		code, decoded, err := post(path, token, body)
		if err != nil {
			return nil, err
		}
		if code == http.StatusTooManyRequests {
			time.Sleep(2 * time.Second)
			continue
		}
		if code != http.StatusOK {
			return decoded, fmt.Errorf("%s returned %d: %v", path, code, decoded)
		}
		return decoded, nil
	}
}

type benchUser struct {
	email  string
	token  string
	userID float64
	peerID float64
}

func data(payload map[string]any) map[string]any {
	d, _ := payload["data"].(map[string]any)
	return d
}

// ====== 压测消息体 ======

type benchContent struct {
	Bench  bool  `json:"bench"`
	SentAt int64 `json:"sentAt"`
	Seq    int64 `json:"seq"`
}

func main() {
	flag.Parse()
	if *users%2 != 0 {
		fmt.Println("users 必须为偶数")
		os.Exit(1)
	}

	fmt.Printf("== 准备阶段：%d 用户 / %d 设备 / %d 好友对 ==\n", *users, *devices, *users/2)
	pool := setupUsers()
	setupFriendPairs(pool)

	fmt.Printf("== 建连阶段：%d 条 WebSocket（%d conn/s）==\n", *users**devices, *dialRate)
	conns := dialAll(pool)
	defer func() {
		for _, c := range conns {
			_ = c.ws.Close()
		}
	}()
	fmt.Printf("连接成功 %d / 失败 %d，平均建连耗时 %.1fms\n",
		m.connOK.Load(), m.connFail.Load(),
		float64(m.connSetupNs.Load())/float64(max64(m.connOK.Load(), 1))/1e6)

	fmt.Printf("== 发压阶段：每对 %.1f msg/s × %v ==\n", *rate, *duration)
	runLoad(pool, conns)

	// 等在途消息投递完
	time.Sleep(2 * time.Second)
	report()
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func setupUsers() []*benchUser {
	pool := make([]*benchUser, *users)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i := range pool {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			u := &benchUser{email: fmt.Sprintf("bench-%04d@test.com", i)}
			// 幂等：已存在则直接登录，重复运行不付出注册成本。
			_, _ = postRetry429("/v1/user/register", "", map[string]any{
				"email": u.email, "password": "bench-pass-123", "nickname": fmt.Sprintf("bench%04d", i),
			})
			login, err := postRetry429("/v1/user/login", "", map[string]any{
				"email": u.email, "password": "bench-pass-123",
			})
			if err != nil {
				fmt.Println("login failed:", err)
				os.Exit(1)
			}
			u.token = data(login)["token"].(string)
			pool[i] = u
		}(i)
	}
	wg.Wait()
	return pool
}

func setupFriendPairs(pool []*benchUser) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := 0; i < len(pool); i += 2 {
		wg.Add(1)
		sem <- struct{}{}
		go func(a, b *benchUser) {
			defer wg.Done()
			defer func() { <-sem }()
			// 幂等：已是好友时 add 返回冲突，忽略即可。
			_, _, _ = post("/v1/friend/add", a.token, map[string]any{"friendEmail": b.email})
			if pending, err := postRetry429("/v1/friend/pending", b.token, map[string]any{}); err == nil {
				if list, _ := pending["data"].([]any); len(list) > 0 {
					for _, item := range list {
						req := item.(map[string]any)
						_, _, _ = post("/v1/friend/accept", b.token, map[string]any{"requestID": req["requestID"]})
					}
				}
			}
			// 从好友列表解析双方 userID。
			fa, _ := postRetry429("/v1/friend/list", a.token, map[string]any{})
			for _, f := range fa["data"].([]any) {
				a.peerID = f.(map[string]any)["userID"].(float64)
			}
			fb, _ := postRetry429("/v1/friend/list", b.token, map[string]any{})
			for _, f := range fb["data"].([]any) {
				b.peerID = f.(map[string]any)["userID"].(float64)
			}
			a.userID, b.userID = b.peerID, a.peerID
		}(pool[i], pool[i+1])
	}
	wg.Wait()
}

type benchConn struct {
	ws    *websocket.Conn
	owner *benchUser
	// pendingAcks: clientMsgID → 发送时间，reader 收到 ACK 后记延迟。
	mu      sync.Mutex
	pending map[string]time.Time
}

func dialAll(pool []*benchUser) []*benchConn {
	wsURL := strings.Replace(*base, "http", "ws", 1) + "/v1/ws"
	conns := make([]*benchConn, 0, len(pool)**devices)
	ticker := time.NewTicker(time.Second / time.Duration(*dialRate))
	defer ticker.Stop()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, u := range pool {
		for d := 0; d < *devices; d++ {
			<-ticker.C
			wg.Add(1)
			go func(u *benchUser) {
				defer wg.Done()
				start := time.Now()
				dialer := websocket.Dialer{
					Subprotocols:     []string{"chat", "bearer." + u.token},
					HandshakeTimeout: 10 * time.Second,
				}
				ws, _, err := dialer.Dial(wsURL, nil)
				if err != nil {
					m.connFail.Add(1)
					return
				}
				m.connOK.Add(1)
				m.connSetupNs.Add(int64(time.Since(start)))
				c := &benchConn{ws: ws, owner: u, pending: map[string]time.Time{}}
				mu.Lock()
				conns = append(conns, c)
				mu.Unlock()
				go c.readLoop()
			}(u)
		}
	}
	wg.Wait()
	return conns
}

func (c *benchConn) readLoop() {
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(120 * time.Second))
		var payload map[string]any
		if err := c.ws.ReadJSON(&payload); err != nil {
			return
		}
		switch payload["type"] {
		case "message_ack":
			id, _ := data(payload)["clientMsgID"].(string)
			c.mu.Lock()
			if sentAt, ok := c.pending[id]; ok {
				delete(c.pending, id)
				c.mu.Unlock()
				m.addAck(time.Since(sentAt))
			} else {
				c.mu.Unlock()
			}
		case "message":
			content, _ := data(payload)["content"].(string)
			var bc benchContent
			if json.Unmarshal([]byte(content), &bc) == nil && bc.Bench {
				m.addE2E(time.Since(time.Unix(0, bc.SentAt)))
			}
		case "error":
			m.readErrs.Add(1)
		}
	}
}

func runLoad(pool []*benchUser, conns []*benchConn) {
	// 每用户 0 号连接作为发送设备。
	senderOf := map[*benchUser]*benchConn{}
	for _, c := range conns {
		if _, ok := senderOf[c.owner]; !ok {
			senderOf[c.owner] = c
		}
	}

	var wg sync.WaitGroup
	stop := time.After(*duration)
	stopCh := make(chan struct{})
	go func() { <-stop; close(stopCh) }()

	var seq atomic.Int64
	for _, u := range pool {
		sender, ok := senderOf[u]
		if !ok || u.peerID == 0 {
			continue
		}
		wg.Add(1)
		go func(u *benchUser, sender *benchConn) {
			defer wg.Done()
			interval := time.Duration(float64(time.Second) / *rate)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					n := seq.Add(1)
					content, _ := json.Marshal(benchContent{Bench: true, SentAt: time.Now().UnixNano(), Seq: n})
					clientMsgID := fmt.Sprintf("bench-%d", n)
					sender.mu.Lock()
					sender.pending[clientMsgID] = time.Now()
					sender.mu.Unlock()
					err := sender.ws.WriteJSON(map[string]any{
						"type": "message", "clientMsgID": clientMsgID,
						"targetType": "private", "targetID": u.peerID, "content": string(content),
					})
					if err != nil {
						m.sendErrs.Add(1)
						return
					}
					m.sent.Add(1)
				}
			}
		}(u, sender)
	}
	wg.Wait()
}

func report() {
	ackP50, ackP95, ackP99, ackMax := percentiles(m.ackLatency)
	e2eP50, e2eP95, e2eP99, e2eMax := percentiles(m.e2eLatency)
	secs := duration.Seconds()

	fmt.Println()
	fmt.Println("================ 压测报告 ================")
	fmt.Printf("拓扑           : %d 用户 × %d 设备 = %d 并发 WebSocket 连接\n", *users, *devices, m.connOK.Load())
	fmt.Printf("负载           : %d 好友对 × %.1f msg/s × %v\n", *users/2, *rate, *duration)
	fmt.Printf("发送           : %d 条（发送错误 %d）\n", m.sent.Load(), m.sendErrs.Load())
	fmt.Printf("ACK            : %d 条（%.1f%%）  吞吐 %.0f msg/s\n",
		m.acked.Load(), 100*float64(m.acked.Load())/float64(max64(m.sent.Load(), 1)), float64(m.acked.Load())/secs)
	fmt.Printf("投递           : %d 次推送  吞吐 %.0f 投递/s（扇出≈%d/条）\n",
		m.delivered.Load(), float64(m.delivered.Load())/secs, 2**devices-1)
	fmt.Printf("ACK 延迟       : P50 %v  P95 %v  P99 %v  MAX %v\n", ackP50, ackP95, ackP99, ackMax)
	fmt.Printf("端到端投递延迟 : P50 %v  P95 %v  P99 %v  MAX %v\n", e2eP50, e2eP95, e2eP99, e2eMax)
	fmt.Printf("WS 业务错误    : %d\n", m.readErrs.Load())
	fmt.Println("==========================================")
}
