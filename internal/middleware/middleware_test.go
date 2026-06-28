package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chat_proj/internal/auth"
	"chat_proj/internal/ratelimit"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestAuthRequiredAllowsPublicRegisterAndLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(AuthRequired())
	r.POST("/v1/user/register", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.POST("/v1/user/login", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.POST("/v1/user/refresh", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for _, path := range []string{"/v1/user/register", "/v1/user/login", "/v1/user/refresh"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected %s to pass without token, got %d", path, w.Code)
		}
	}
}

func TestAuthRequiredRejectsProtectedRouteWithoutToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, logs := observer.New(zap.WarnLevel)
	logger.Logger = zap.New(core)

	r := gin.New()
	r.Use(RequestID())
	r.Use(AuthRequired())
	r.POST("/v1/user/update", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/user/update", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", w.Code)
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected one auth error log, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if entries[0].Message != "HTTPBusinessError" ||
		fields["code"] != "unauthorized" ||
		fields["status"] != int64(http.StatusUnauthorized) ||
		fields["path"] != "/v1/user/update" {
		t.Fatalf("unexpected auth error log: %+v", fields)
	}
}

func TestAuthRequiredSetsUserIDForValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	if err := auth.Init("middleware-test-secret"); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	token, _, err := auth.GenerateAccessToken(42, "alice")
	if err != nil {
		t.Fatalf("GenerateAccessToken returned error: %v", err)
	}

	r := gin.New()
	r.Use(AuthRequired())
	r.POST("/v1/user/update", func(c *gin.Context) {
		if c.GetUint("user_id") != 42 {
			t.Fatalf("expected user_id 42, got %d", c.GetUint("user_id"))
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/user/update", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d", w.Code)
	}
}

func TestRateLimitRejectsRequestsOverLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(RequestID())
	r.Use(RateLimit(ratelimit.NewMemoryLimiter(), 2, time.Minute))
	r.GET("/limited", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/limited", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected request %d to pass, got %d", i+1, w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/limited", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected too many requests, got %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got == "" {
		t.Fatal("expected Retry-After header")
	}
}

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(CORS([]string{"https://chat.example.com"}))
	r.POST("/v1/user/login", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/user/login", nil)
	req.Header.Set("Origin", "https://chat.example.com")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://chat.example.com" {
		t.Fatalf("expected allowed origin header, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("expected allowed headers")
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Content-SHA256") {
		t.Fatalf("expected exposed sha256 header, got %q", got)
	}
}

func TestCORSRejectsUnknownOriginHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(CORS([]string{"http://localhost:5173"}))
	r.POST("/v1/user/login", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/user/login", nil)
	req.Header.Set("Origin", "https://unknown.example.com")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no allow-origin header for unknown origin, got %q", got)
	}
}

func TestCORSShortCircuitsPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(CORS([]string{"http://localhost:5173"}))
	r.Use(AuthRequired())
	r.OPTIONS("/v1/user/update", func(c *gin.Context) {
		t.Fatal("preflight should not reach route handler")
	})

	req := httptest.NewRequest(http.MethodOptions, "/v1/user/update", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected no content, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("expected allowed origin header, got %q", got)
	}
}

func TestRequestIDSetsHeaderAndContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(RequestID())
	r.GET("/ping", func(c *gin.Context) {
		if c.GetString("request_id") == "" {
			t.Fatal("expected request_id in context")
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header")
	}
}

func TestRequestIDPreservesIncomingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(RequestID())
	r.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-ID", "req-custom")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if got := w.Header().Get("X-Request-ID"); got != "req-custom" {
		t.Fatalf("expected incoming request id to be preserved, got %q", got)
	}
}
