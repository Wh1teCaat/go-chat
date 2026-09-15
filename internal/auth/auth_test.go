package auth

import (
	"strings"
	"testing"
)

func TestValidateTokenRejectsUnknownExpectedType(t *testing.T) {
	if err := Init("test-secret"); err != nil {
		t.Fatal(err)
	}
	token, _, err := GenerateAccessToken(42, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []TokenType{"", "unknown"} {
		if _, err := ValidateToken(token, typ); err == nil {
			t.Fatalf("expected rejection for type %q", typ)
		}
	}
}

func TestPackageFunctionsUseInitializedSecret(t *testing.T) {
	if err := Init("secret-a"); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	token, _, err := GenerateAccessToken(42, "alice")
	if err != nil {
		t.Fatalf("GenerateAccessToken returned error: %v", err)
	}

	claims, err := ValidateToken(token, TokenTypeAccess)
	if err != nil {
		t.Fatalf("ValidateToken with same secret returned error: %v", err)
	}
	if claims.UserID != 42 || claims.Username != "alice" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	if err := Init("secret-b"); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if _, err := ValidateToken(token, TokenTypeAccess); err == nil || !strings.Contains(err.Error(), "invalid token signature") {
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

	claims, err := ValidateToken(refreshToken, TokenTypeRefresh)
	if err != nil {
		t.Fatalf("ValidateToken refresh returned error: %v", err)
	}
	if claims.ID != jti {
		t.Fatalf("expected claims jti %q, got %q", jti, claims.ID)
	}
	if _, err := ValidateToken(accessToken, TokenTypeRefresh); err == nil || !strings.Contains(err.Error(), "invalid token type") {
		t.Fatalf("expected access token to be rejected as refresh token, got %v", err)
	}
	if _, err := ValidateToken(refreshToken, TokenTypeAccess); err == nil || !strings.Contains(err.Error(), "invalid token type") {
		t.Fatalf("expected refresh token to be rejected as access token, got %v", err)
	}
}

func TestInitRejectsEmptySecret(t *testing.T) {
	if err := Init(" "); err == nil {
		t.Fatal("expected error for empty secret")
	}
}
