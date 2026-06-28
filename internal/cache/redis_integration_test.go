package cache

import (
	"context"
	"os"
	"testing"
	"time"

	"chat_proj/internal/config"
)

func TestRedisStoreRealConnection(t *testing.T) {
	if os.Getenv("CHAT_REDIS_INTEGRATION") != "1" {
		t.Skip("set CHAT_REDIS_INTEGRATION=1 to run real redis integration test")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if !cfg.Redis.Enabled {
		t.Fatal("redis is disabled by config")
	}

	client, err := NewRedisClient(context.Background(), cfg.Redis)
	if err != nil {
		t.Fatalf("NewRedisClient returned error: %v", err)
	}
	defer client.Close()

	store := NewRedisStore(client)
	key := "chat_proj:test:redis_store"
	value := struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}{
		Name: "ok",
		N:    1,
	}

	if err := store.SetJSON(context.Background(), key, value, 30*time.Second); err != nil {
		t.Fatalf("SetJSON returned error: %v", err)
	}

	var got struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	ok, err := store.GetJSON(context.Background(), key, &got)
	if err != nil {
		t.Fatalf("GetJSON returned error: %v", err)
	}
	if !ok || got.Name != value.Name || got.N != value.N {
		t.Fatalf("unexpected cached value: ok=%v got=%+v", ok, got)
	}

	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	ok, err = store.GetJSON(context.Background(), key, &got)
	if err != nil {
		t.Fatalf("GetJSON after delete returned error: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss after delete")
	}
}
