package cache

import (
	"chat_proj/internal/testutil"
	"context"
	"testing"
	"time"
)

func TestRotationPreflightPreservesOldToken(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"collision", "wrong-type", "ttl"} {
		t.Run(failure, func(t *testing.T) {
			client := testutil.Redis(t)
			store := NewRedisStore(client)
			client.Set(ctx, "old", "42", time.Minute)
			input := TokenRotation{OldKey: "old", NewKey: "new", ResultKey: "result", UserID: "42", RequestID: "request", EncryptedResult: "encrypted", TokenTTL: time.Hour, ResultTTL: time.Minute}
			switch failure {
			case "collision":
				client.Set(ctx, "new", "other", time.Hour)
			case "wrong-type":
				client.HSet(ctx, "result", "bad", "type")
			case "ttl":
				input.ResultTTL = 0
			}
			if result, err := store.RotateToken(ctx, input); err == nil || result != nil {
				t.Fatal("expected failure")
			}
			if client.Get(ctx, "old").Val() != "42" {
				t.Fatal("old token lost")
			}
			if failure != "collision" && client.Exists(ctx, "new").Val() != 0 {
				t.Fatal("successor leaked")
			}
		})
	}
}
