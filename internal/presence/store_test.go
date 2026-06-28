package presence

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreTracksMultipleConnections(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.Connect(ctx, 7, "conn-a"); err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if err := store.Connect(ctx, 7, "conn-b"); err != nil {
		t.Fatalf("Connect second returned error: %v", err)
	}

	online, err := store.ListOnline(ctx, []uint{7})
	if err != nil {
		t.Fatalf("ListOnline returned error: %v", err)
	}
	if !online[7] {
		t.Fatal("expected user to be online")
	}

	if err := store.Disconnect(ctx, 7, "conn-a"); err != nil {
		t.Fatalf("Disconnect returned error: %v", err)
	}
	online, err = store.ListOnline(ctx, []uint{7})
	if err != nil {
		t.Fatalf("ListOnline after first disconnect returned error: %v", err)
	}
	if !online[7] {
		t.Fatal("expected user to stay online while another connection exists")
	}

	if err := store.Disconnect(ctx, 7, "conn-b"); err != nil {
		t.Fatalf("Disconnect second returned error: %v", err)
	}
	online, err = store.ListOnline(ctx, []uint{7})
	if err != nil {
		t.Fatalf("ListOnline after all disconnects returned error: %v", err)
	}
	if online[7] {
		t.Fatal("expected user to be offline")
	}
}

func TestMemoryStoreExpiresStaleConnection(t *testing.T) {
	store := newMemoryStore(10 * time.Millisecond)
	ctx := context.Background()

	if err := store.Connect(ctx, 8, "conn-a"); err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	online, err := store.ListOnline(ctx, []uint{8})
	if err != nil {
		t.Fatalf("ListOnline returned error: %v", err)
	}
	if online[8] {
		t.Fatal("expected stale connection to expire")
	}
}
