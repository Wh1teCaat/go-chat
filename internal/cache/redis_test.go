package cache

import (
	"chat_proj/internal/testutil"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashUpdatePreservesFieldsAndString(t *testing.T) {
	ctx := context.Background()
	client := testutil.Redis(t)
	s := NewRedisStore(client)
	if err := s.SetHash(ctx, "profile", map[string]string{"id": "42", "obsolete": "x"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHash(ctx, "profile", map[string]string{"id": "42"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	fields, ok, err := s.GetHash(ctx, "profile")
	if err != nil || !ok || len(fields) != 2 || fields["id"] != "42" || fields["obsolete"] != "x" {
		t.Fatalf("%v %v %v", fields, ok, err)
	}
	if ttl := client.PTTL(ctx, "profile").Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatal(ttl)
	}
	if err := s.SetString(ctx, "empty", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	value, ok, err := s.GetString(ctx, "empty")
	if err != nil || !ok || value != "" {
		t.Fatal(value, ok, err)
	}
	if err := s.Delete(ctx, "profile"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = s.GetHash(ctx, "profile")
	if err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestRotateStringConcurrent(t *testing.T) {
	ctx := context.Background()
	s := NewRedisStore(testutil.Redis(t))
	if err := s.SetString(ctx, "old", "42", time.Minute); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := s.RotateString(ctx, "old", fmt.Sprintf("new:%d", i), "42", "42", 7*24*time.Hour)
			if err != nil {
				t.Error(err)
			}
			if ok {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
	_, ok, err := s.GetString(ctx, "old")
	if err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestRotateStringRejectsMismatchAndCollision(t *testing.T) {
	ctx := context.Background()
	s := NewRedisStore(testutil.Redis(t))
	_ = s.SetString(ctx, "old", "42", time.Minute)
	ok, err := s.RotateString(ctx, "old", "new", "99", "99", time.Minute)
	if err != nil || ok {
		t.Fatal(ok, err)
	}
	_ = s.SetString(ctx, "new", "other", time.Minute)
	ok, err = s.RotateString(ctx, "old", "new", "42", "42", time.Minute)
	if err == nil || ok {
		t.Fatal(ok, err)
	}
	v, found, err := s.GetString(ctx, "old")
	if err != nil || !found || v != "42" {
		t.Fatal(v, found, err)
	}
	v, _, _ = s.GetString(ctx, "new")
	if v != "other" {
		t.Fatal(v)
	}
}
