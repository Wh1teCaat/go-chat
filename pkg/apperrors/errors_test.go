package apperrors

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestHTTPCodeMapsBusinessErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid input", err: ErrInvalidInput, want: http.StatusBadRequest},
		{name: "wrapped invalid input", err: fmt.Errorf("%w: bad target", ErrInvalidInput), want: http.StatusBadRequest},
		{name: "permission denied", err: ErrPermissionDenied, want: http.StatusForbidden},
		{name: "not found", err: ErrNotFound, want: http.StatusNotFound},
		{name: "user not found", err: ErrUserNotFound, want: http.StatusNotFound},
		{name: "wrong password", err: ErrWrongPassword, want: http.StatusUnauthorized},
		{name: "missing token", err: ErrUnauthorized, want: http.StatusUnauthorized},
		{name: "invalid token", err: ErrInvalidToken, want: http.StatusUnauthorized},
		{name: "conflict", err: ErrEmailAlreadyExists, want: http.StatusConflict},
		{name: "internal", err: ErrDBOperation, want: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTTPCode(tt.err); got != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, got)
			}
			if code := Code(tt.err); code == "" {
				t.Fatal("expected non-empty error code")
			}
		})
	}
}

func TestWithMessageKeepsKindForMapping(t *testing.T) {
	err := WithMessage(ErrInvalidInput, "cannot transfer ownership to self")

	if err.Error() != "cannot transfer ownership to self" {
		t.Fatalf("expected custom message, got %q", err.Error())
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatal("expected wrapped invalid input cause")
	}
	if got := HTTPCode(err); got != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", got)
	}
	if got := Code(err); got != "invalid_input" {
		t.Fatalf("expected invalid_input code, got %q", got)
	}
	if cause := Cause(err); cause != nil {
		t.Fatalf("expected nil cause, got %v", cause)
	}
}

func TestWithCauseKeepsSafeMessageAndOriginalCause(t *testing.T) {
	dbErr := errors.New("UNIQUE constraint failed: users.email")
	err := WithCause(ErrDBOperation, "database operation failed", dbErr)

	if err.Error() != "database operation failed" {
		t.Fatalf("expected safe message, got %q", err.Error())
	}
	if !errors.Is(err, ErrDBOperation) {
		t.Fatal("expected wrapped database operation kind")
	}
	if got := HTTPCode(err); got != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500, got %d", got)
	}
	if cause := Cause(err); cause != dbErr {
		t.Fatalf("expected original cause, got %v", cause)
	}
}
