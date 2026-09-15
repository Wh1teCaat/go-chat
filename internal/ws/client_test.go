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

// blockedPresenceStore simulates slow cleanup without delaying message delivery.
type blockedPresenceStore struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockedPresenceStore) Connect(context.Context, uint, string) error { return nil }
func (s *blockedPresenceStore) Refresh(context.Context, uint, string) error { return nil }
func (s *blockedPresenceStore) Disconnect(context.Context, uint, string) error {
	close(s.entered)
	<-s.release
	return nil
}

func TestFullQueueCleanupDoesNotBlockBroadcast(t *testing.T) {
	hub := NewHub()
	store := &blockedPresenceStore{entered: make(chan struct{}), release: make(chan struct{})}
	hub.SetPresenceStore(store)
	connections := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		connections <- conn
	}))
	defer server.Close()
	peer, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	slow := NewClient(1, <-connections, hub, nil)
	defer func() {
		close(store.release)
		slow.Close() // Wait for the asynchronous cleanup before leaving the test.
	}()
	fast := NewClient(2, nil, hub, nil)
	hub.Add(slow)
	hub.Add(fast)
	for i := 0; i < cap(slow.send); i++ {
		slow.Send(i)
	}
	done := make(chan struct{})
	go func() {
		hub.SendToMany([]uint{1, 2}, "first")
		hub.SendToMany([]uint{1, 2}, "second")
		close(done)
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow cleanup blocked broadcast")
	}
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-fast.send:
			if got != want {
				t.Fatalf("got %v, want %s", got, want)
			}
		default:
			t.Fatalf("missing message %s", want)
		}
	}
	// Further sends to the closing client must return without waiting for cleanup.
	repeated := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			slow.Send(i)
		}
		close(repeated)
	}()
	select {
	case <-repeated:
	case <-time.After(time.Second):
		t.Fatal("send to closing client blocked")
	}
}
