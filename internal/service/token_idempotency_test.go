package service

import (
	"chat_proj/internal/auth"
	"chat_proj/internal/cache"
	"chat_proj/pkg/apperrors"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const testRequestID = "0123456789abcdef0123456789abcdef"

func tokenFixture(t *testing.T) (*miniredis.Miniredis, *redis.Client, *TokenPair) {
	t.Helper()
	if err := auth.Init("idempotent-refresh-test-secret"); err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { client.Close() })
	InitTokenStore(cache.NewRedisStore(client))
	pair, err := TokenService.IssueTokenPair(context.Background(), 42, "alice")
	if err != nil {
		t.Fatal(err)
	}
	return server, client, pair
}
func TestRefreshRecoversOriginalResultWithoutExtendingTTL(t *testing.T) {
	server, _, old := tokenFixture(t)
	first, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID)
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := auth.ValidateToken(old.RefreshToken, auth.TokenTypeRefresh)
	raw, err := server.Get(refreshResultKey(claims.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, first.AccessToken) || strings.Contains(raw, first.RefreshToken) {
		t.Fatal("plaintext bearer credential")
	}
	server.FastForward(10 * time.Second)
	again, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("lost-response recovery: %v", err)
	}
	if ttl := server.TTL(refreshResultKey(claims.ID)); ttl != 50*time.Second {
		t.Fatalf("extended TTL: %s", ttl)
	}
	server.FastForward(51 * time.Second)
	if _, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID); !errors.Is(err, apperrors.ErrInvalidToken) {
		t.Fatalf("expired result: %v", err)
	}
	if _, err := TokenService.RefreshTokenPair(context.Background(), first.RefreshToken, strings.Repeat("a", 32)); err != nil {
		t.Fatalf("successor: %v", err)
	}
}
func TestConcurrentRefreshSameRequestReturnsOnePair(t *testing.T) {
	server, _, old := tokenFixture(t)
	results := make([]*TokenPair, 20)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pair, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID)
			if err != nil {
				t.Errorf("refresh: %v", err)
			}
			results[i] = pair
		}(i)
	}
	wg.Wait()
	for _, pair := range results {
		if pair == nil || !reflect.DeepEqual(results[0], pair) {
			t.Fatal("different results")
		}
	}
	if len(server.Keys()) != 2 {
		t.Fatalf("candidate keys leaked: %v", server.Keys())
	}
}
func TestConcurrentDifferentRequestsOnlyOneWins(t *testing.T) {
	server, _, old := tokenFixture(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, fmt.Sprintf("%032x", i))
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, apperrors.ErrInvalidToken) {
				t.Errorf("error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 || len(server.Keys()) != 2 {
		t.Fatalf("successes=%d keys=%v", successes.Load(), server.Keys())
	}
}
func TestLogoutAndFurtherRotationPreventCachedRecovery(t *testing.T) {
	for _, action := range []string{"old-logout", "new-logout", "new-rotation"} {
		t.Run(action, func(t *testing.T) {
			_, _, old := tokenFixture(t)
			pair, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "old-logout":
				err = TokenService.RevokeRefreshToken(context.Background(), old.RefreshToken)
			case "new-logout":
				err = TokenService.RevokeRefreshToken(context.Background(), pair.RefreshToken)
			case "new-rotation":
				_, err = TokenService.RefreshTokenPair(context.Background(), pair.RefreshToken, strings.Repeat("b", 32))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID); !errors.Is(err, apperrors.ErrInvalidToken) {
				t.Fatalf("inactive successor recovered: %v", err)
			}
			if action != "new-rotation" {
				if _, err := TokenService.RefreshTokenPair(context.Background(), pair.RefreshToken, strings.Repeat("c", 32)); !errors.Is(err, apperrors.ErrInvalidToken) {
					t.Fatalf("successor still active: %v", err)
				}
			}
		})
	}
}
func TestRotationRacingOldTokenLogout(t *testing.T) {
	for i := 0; i < 20; i++ {
		server, _, old := tokenFixture(t)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID)
			if err != nil && !errors.Is(err, apperrors.ErrInvalidToken) {
				t.Errorf("rotation: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := TokenService.RevokeRefreshToken(context.Background(), old.RefreshToken); err != nil {
				t.Errorf("logout: %v", err)
			}
		}()
		wg.Wait()
		if len(server.Keys()) != 0 {
			t.Fatalf("remaining keys=%v", server.Keys())
		}
	}
}

type lostRotationReplyStore struct {
	cache.TokenStore
	lost atomic.Bool
}

func (s *lostRotationReplyStore) RotateToken(ctx context.Context, input cache.TokenRotation) (*cache.TokenRotationResult, error) {
	result, err := s.TokenStore.RotateToken(ctx, input)
	if err == nil && s.lost.CompareAndSwap(false, true) {
		return nil, errors.New("lost Redis reply")
	}
	return result, err
}
func TestRetryAfterRedisReplyLost(t *testing.T) {
	server, client, old := tokenFixture(t)
	InitTokenStore(&lostRotationReplyStore{TokenStore: cache.NewRedisStore(client)})
	if _, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID); !errors.Is(err, apperrors.ErrTokenOperation) {
		t.Fatalf("transient error: %v", err)
	}
	claims, _ := auth.ValidateToken(old.RefreshToken, auth.TokenTypeRefresh)
	raw, _ := server.Get(refreshResultKey(claims.ID))
	var record cache.TokenRotationResult
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	plaintext, err := auth.OpenRefreshResult(record.EncryptedResult, claims.ID+":"+testRequestID+":42")
	if err != nil {
		t.Fatal(err)
	}
	var expected TokenPair
	json.Unmarshal(plaintext, &expected)
	got, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID)
	if err != nil || !reflect.DeepEqual(&expected, got) {
		t.Fatalf("retry: %v", err)
	}
	if len(server.Keys()) != 2 {
		t.Fatal("duplicate rotation")
	}
}
