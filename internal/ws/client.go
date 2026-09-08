package ws

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type IncomingHandler func(ctx context.Context, userID uint, payload []byte) error

var (
	// pongWait 表示连接多久没收到 pong 就认为超时。首次 read deadline 也要设置，
	// 否则对端静默时，第一次 pong 到来前 ReadMessage 可能一直阻塞。
	pongWait   = 60 * time.Second
	pingPeriod = 54 * time.Second
	writeWait  = 10 * time.Second
)

type Client struct {
	UserID uint

	conn         *websocket.Conn
	hub          *Hub
	handle       IncomingHandler
	connectionID string
	// send 是 hub/controller 写入该 websocket 的唯一入口，
	// writeLoop 负责把 channel 中的消息串行写到连接上。
	send   chan any
	mu     sync.Mutex
	closed bool
	// once 保证 Close 幂等。readLoop、writeLoop、Send 队列满和测试都可能触发关闭。
	once sync.Once
}

func NewClient(userID uint, conn *websocket.Conn, hub *Hub, handle IncomingHandler) *Client {
	return &Client{
		UserID:       userID,
		conn:         conn,
		hub:          hub,
		handle:       handle,
		connectionID: newConnectionID(),
		send:         make(chan any, 32),
	}
}

// Start 注册客户端并启动独立的读写循环，直到连接结束。
func (c *Client) Start(ctx context.Context) {
	c.hub.Add(c)
	c.markPresenceConnected(ctx)
	go c.writeLoop()
	// readLoop 留在请求 goroutine 中执行；它返回时通过 defer Close 移除连接并关闭写循环。
	c.readLoop(ctx)
}

// Send 将消息放入客户端发送队列，队列阻塞时关闭连接以保护服务。
func (c *Client) Send(message any) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}

	select {
	case c.send <- message:
		c.mu.Unlock()
	default:
		// send 缓冲满说明该连接消费服务端推送太慢，关闭它避免慢连接持续占用内存。
		c.mu.Unlock()
		c.Close()
	}
}

// readLoop 持续读取客户端消息、刷新读期限并交给业务处理器。
func (c *Client) readLoop(ctx context.Context) {
	defer c.Close()

	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if c.handle != nil {
			_ = c.handle(ctx, c.UserID, payload)
		}
	}
}

// writeLoop 持续发送队列消息和心跳，直到连接关闭。
func (c *Client) writeLoop() {
	defer c.Close()

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				return
			}
			if err := c.writeJSON(message); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.writePing(); err != nil {
				return
			}
			c.refreshPresence()
		}
	}
}

// writeJSON 在写入期限内向 WebSocket 连接发送 JSON 消息。
func (c *Client) writeJSON(message any) error {
	// 写超时作用于本次 WriteJSON，避免网络写阻塞导致 writeLoop 永久挂住。
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.conn.WriteJSON(message)
}

// writePing 在写入期限内向客户端发送 WebSocket 心跳帧。
func (c *Client) writePing() error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.PingMessage, nil)
}

// Close 以幂等方式关闭客户端发送队列和网络连接。
func (c *Client) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.send)
		c.mu.Unlock()

		c.hub.Remove(c)
		c.markPresenceDisconnected()
		_ = c.conn.Close()
	})
}

// markPresenceConnected 在状态存储中登记当前连接上线。
func (c *Client) markPresenceConnected(ctx context.Context) {
	store := c.hub.PresenceStore()
	if store == nil {
		return
	}
	_ = store.Connect(ctx, c.UserID, c.connectionID)
}

// markPresenceDisconnected 在独立超时上下文中登记当前连接下线。
func (c *Client) markPresenceDisconnected() {
	store := c.hub.PresenceStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = store.Disconnect(ctx, c.UserID, c.connectionID)
}

// refreshPresence 在独立超时上下文中续期当前连接的在线状态。
func (c *Client) refreshPresence() {
	store := c.hub.PresenceStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = store.Refresh(ctx, c.UserID, c.connectionID)
}
