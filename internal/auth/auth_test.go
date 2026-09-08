package auth

import (
	"strings"
	"testing"
)

func TestPackageFunctionsUseInitializedSecret(t *testing.T) {
	if err := Init("secret-a"); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	token, _, err := GenerateAccessToken(42, "alice")
	if err != nil {
		t.Fatalf("GenerateAccessToken returned error: %v", err)
	}

	claims, err := ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken with same secret returned error: %v", err)
	}
	if claims.UserID != 42 || claims.Username != "alice" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	if err := Init("secret-b"); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if _, err := ValidateToken(token); err == nil || !strings.Contains(err.Error(), "invalid token signature") {
		t.Fatalf("expected invalid token signature with different secret, got %v", err)
	}
}

func TestRefreshTokenIsSeparateFromAccessToken(t *testing.T) {
	if err := Init("refresh-secret"); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	accessToken, _, err := GenerateAccessToken(42, "alice")
	if err != nil {
		t.Fatalf("GenerateAccessToken returned error: %v", err)
	}
	refreshToken, _, jti, err := GenerateRefreshToken(42, "alice")
	if err != nil {
		t.Fatalf("GenerateRefreshToken returned error: %v", err)
	}
	if jti == "" {
		t.Fatal("expected refresh token to carry a jti")
	}

	claims, err := ValidateRefreshToken(refreshToken)
	if err != nil {
		t.Fatalf("ValidateRefreshToken returned error: %v", err)
	}
	if claims.ID != jti {
		t.Fatalf("expected claims jti %q, got %q", jti, claims.ID)
	}
	if _, err := ValidateRefreshToken(accessToken); err == nil || !strings.Contains(err.Error(), "invalid token type") {
		t.Fatalf("expected access token to be rejected as refresh token, got %v", err)
	}
	if _, err := ValidateToken(refreshToken); err == nil || !strings.Contains(err.Error(), "invalid token type") {
		t.Fatalf("expected refresh token to be rejected as access token, got %v", err)
	}
}

func TestInitRejectsEmptySecret(t *testing.T) {
	if err := Init(" "); err == nil {
		t.Fatal("expected error for empty secret")
	}
}
