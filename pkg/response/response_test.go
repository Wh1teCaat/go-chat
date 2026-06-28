package response

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"chat_proj/pkg/apperrors"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestErrorLogsBusinessErrorWithRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, logs := observer.New(zap.WarnLevel)
	logger.Logger = zap.New(core)

	r := gin.New()
	r.GET("/boom", func(c *gin.Context) {
		c.Set(RequestIDKey, "req-test")
		c.Set("user_id", uint(42))
		Error(c, apperrors.ErrPermissionDenied)
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden, got %d", w.Code)
	}
	var body Body
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Code != http.StatusForbidden {
		t.Fatalf("expected body code %d, got %d", http.StatusForbidden, body.Code)
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected one log entry, got %d", len(entries))
	}
	if entries[0].Level != zapcore.WarnLevel {
		t.Fatalf("expected warn log, got %s", entries[0].Level)
	}
	fields := entries[0].ContextMap()
	if fields["request_id"] != "req-test" ||
		fields["code"] != "permission_denied" ||
		fields["status"] != int64(http.StatusForbidden) ||
		fields["method"] != http.MethodGet ||
		fields["path"] != "/boom" ||
		fields["user_id"] != uint64(42) {
		t.Fatalf("unexpected log fields: %+v", fields)
	}
}

func TestErrorLogsCauseWhenPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, logs := observer.New(zap.ErrorLevel)
	logger.Logger = zap.New(core)

	r := gin.New()
	r.GET("/boom", func(c *gin.Context) {
		c.Set(RequestIDKey, "req-cause")
		Error(c, apperrors.WithCause(apperrors.ErrDBOperation, "database operation failed", errors.New("db is down")))
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected internal server error, got %d", w.Code)
	}
	fields := logs.All()[0].ContextMap()
	if fields["error"] != "database operation failed" || fields["cause"] != "db is down" {
		t.Fatalf("unexpected log fields: %+v", fields)
	}
}

func TestBindErrorLogsRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, logs := observer.New(zap.WarnLevel)
	logger.Logger = zap.New(core)

	r := gin.New()
	r.POST("/bind", func(c *gin.Context) {
		c.Set(RequestIDKey, "req-bind")
		BindError(c, errors.New("invalid json"))
	})

	req := httptest.NewRequest(http.MethodPost, "/bind", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d", w.Code)
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected one log entry, got %d", len(entries))
	}
	if entries[0].Message != "HTTPBindError" {
		t.Fatalf("expected HTTPBindError log, got %q", entries[0].Message)
	}
	fields := entries[0].ContextMap()
	if fields["request_id"] != "req-bind" ||
		fields["code"] != "bind_error" ||
		fields["status"] != int64(http.StatusBadRequest) ||
		fields["method"] != http.MethodPost ||
		fields["path"] != "/bind" ||
		fields["error"] != "invalid json" {
		t.Fatalf("unexpected log fields: %+v", fields)
	}
}
