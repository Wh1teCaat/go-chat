package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientSendsHeartbeatPing(t *testing.T) {
	originalPingPeriod := pingPeriod
	originalPongWait := pongWait
	pingPeriod = 10 * time.Millisecond
	pongWait = 100 * time.Millisecond
	defer func() {
		pingPeriod = originalPingPeriod
		pongWait = originalPongWait
	}()

	hub := NewHub()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		NewClient(1, conn, hub, nil).Start(context.Background())
	}))
	defer server.Close()

	url := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	pingReceived := make(chan struct{}, 1)
	conn.SetPingHandler(func(appData string) error {
		pingReceived <- struct{}{}
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-pingReceived:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected heartbeat ping")
	}
}
