package ws

import "sync"

type Hub struct {
	mu sync.RWMutex
	// 一个用户可能同时有多个 websocket 连接，例如多标签页、移动端和网页同时在线。
	clients  map[uint]map[*Client]struct{}
	presence PresenceStore
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[uint]map[*Client]struct{}),
	}
}

func (h *Hub) SetPresenceStore(store PresenceStore) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.presence = store
}

func (h *Hub) PresenceStore() PresenceStore {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.presence
}

func (h *Hub) Add(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.clients[client.UserID] == nil {
		h.clients[client.UserID] = make(map[*Client]struct{})
	}
	h.clients[client.UserID][client] = struct{}{}
}

func (h *Hub) Remove(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	userClients := h.clients[client.UserID]
	if userClients == nil {
		return
	}
	delete(userClients, client)
	if len(userClients) == 0 {
		delete(h.clients, client.UserID)
	}
}

func (h *Hub) SendTo(userID uint, message any) bool {
	h.mu.RLock()
	userClients := h.clients[userID]
	// 先复制连接再发送。Client.Send 可能触发关闭并反向注销，持有 hub 锁发送会增加锁冲突风险。
	clients := make([]*Client, 0, len(userClients))
	for client := range userClients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	for _, client := range clients {
		client.Send(message)
	}
	return len(clients) > 0
}

func (h *Hub) SendToMany(userIDs []uint, message any) {
	for _, userID := range userIDs {
		h.SendTo(userID, message)
	}
}

// CloseAll 关闭全部连接，用于优雅停机。websocket 连接在 Upgrade 后已脱离
// net/http 的连接管理，server.Shutdown 不会等待它们，需要显式关闭。
func (h *Hub) CloseAll() {
	h.mu.RLock()
	clients := make([]*Client, 0)
	for _, userClients := range h.clients {
		for client := range userClients {
			clients = append(clients, client)
		}
	}
	h.mu.RUnlock()

	for _, client := range clients {
		client.Close()
	}
}
