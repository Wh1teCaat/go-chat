package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"chat_proj/internal/auth"
	"chat_proj/pkg/apperrors"
	"github.com/golang-jwt/jwt/v5"
)

func TestCachedRecoveryStillValidatesPresentedJWT(t *testing.T) {
	server, _, old := tokenFixture(t)
	claims, err := auth.ValidateToken(old.RefreshToken, auth.TokenTypeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TokenService.RefreshTokenPair(context.Background(), old.RefreshToken, testRequestID); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"signature", "purpose", "expired"} {
		t.Run(failure, func(t *testing.T) {
			altered := *claims
			secret := "idempotent-refresh-test-secret"
			switch failure {
			case "signature":
				secret = "wrong-secret"
			case "purpose":
				altered.TokenType = auth.TokenTypeAccess
			case "expired":
				altered.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
			}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, altered).SignedString([]byte(secret))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := TokenService.RefreshTokenPair(context.Background(), token, testRequestID); !errors.Is(err, apperrors.ErrInvalidToken) {
				t.Fatalf("invalid JWT reached recovery cache: %v", err)
			}
		})
	}
	if len(server.Keys()) != 2 {
		t.Fatal("invalid requests changed rotation state")
	}
}

func TestRecoveryWindowCappedByOldJWTExpiry(t *testing.T) {
	server, _, old := tokenFixture(t)
	claims, err := auth.ValidateToken(old.RefreshToken, auth.TokenTypeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(20 * time.Second))
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("idempotent-refresh-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TokenService.RefreshTokenPair(context.Background(), token, testRequestID); err != nil {
		t.Fatal(err)
	}
	ttl := server.TTL(refreshResultKey(claims.ID))
	if ttl <= 0 || ttl > 20*time.Second {
		t.Fatalf("recovery window exceeds JWT expiry: %s", ttl)
	}
}
