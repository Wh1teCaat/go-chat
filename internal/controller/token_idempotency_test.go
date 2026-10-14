package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"chat_proj/internal/auth"
	"chat_proj/internal/cache"
	"chat_proj/internal/service"
	"chat_proj/internal/testutil"
	"github.com/gin-gonic/gin"
)

func TestRefreshRequestIDHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := auth.Init("controller-idempotency-secret"); err != nil {
		t.Fatal(err)
	}
	service.InitTokenStore(cache.NewRedisStore(testutil.Redis(t)))
	pair, err := service.TokenService.IssueTokenPair(context.Background(), 42, "alice")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/refresh", RefreshToken)
	router.POST("/logout", Logout)
	post := func(path, token, requestID string) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"refreshToken": token, "requestID": requestID})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	// Invalid operation IDs must not consume the refresh token.
	for _, id := range []string{"short", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if w := post("/refresh", pair.RefreshToken, id); w.Code != http.StatusBadRequest {
			t.Fatalf("malformed request ID: status=%d", w.Code)
		}
	}
	const id = "0123456789abcdef0123456789abcdef"
	first := post("/refresh", pair.RefreshToken, id)
	if first.Code != http.StatusOK || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("first refresh: status=%d cache=%q", first.Code, first.Header().Get("Cache-Control"))
	}
	again := post("/refresh", pair.RefreshToken, id)
	if again.Code != http.StatusOK {
		t.Fatalf("recovery: status=%d", again.Code)
	}
	var original, recovered struct{ Data json.RawMessage }
	if err := json.Unmarshal(first.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again.Body.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.Data, recovered.Data) {
		t.Fatal("recovery changed token response")
	}
	for _, otherID := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ""} {
		if w := post("/refresh", pair.RefreshToken, otherID); w.Code != http.StatusUnauthorized {
			t.Fatalf("consumed token with other operation: status=%d", w.Code)
		}
	}
	// A client that lost the response can still log out using its old token.
	if w := post("/logout", pair.RefreshToken, ""); w.Code != http.StatusOK {
		t.Fatalf("logout: status=%d", w.Code)
	}
	if w := post("/refresh", pair.RefreshToken, id); w.Code != http.StatusUnauthorized {
		t.Fatalf("recovered a logged-out session: status=%d", w.Code)
	}
	var body struct {
		Data struct {
			RefreshToken string `json:"refresh_token"`
		}
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w := post("/refresh", body.Data.RefreshToken, id); w.Code != http.StatusUnauthorized {
		t.Fatalf("logout failed to revoke successor: status=%d", w.Code)
	}
}
