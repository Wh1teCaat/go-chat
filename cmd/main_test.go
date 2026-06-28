package main

import (
	"errors"
	"net/http"
	"testing"

	"chat_proj/internal/config"
	"chat_proj/internal/ratelimit"
)

func TestBuildServerUsesConfiguredAddress(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 9090,
		},
	}

	server := buildServer(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	if server.Addr != "127.0.0.1:9090" {
		t.Fatalf("expected configured address, got %q", server.Addr)
	}
}

func TestRunExitsWhenConfigLoadFails(t *testing.T) {
	exitCode := -1

	run(appDeps{
		loadConfig: func() (*config.Config, error) {
			return nil, errors.New("config failed")
		},
		exit: func(code int) {
			exitCode = code
		},
	})

	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
}

func TestInitRateLimiterUsesMemoryWhenRedisDisabled(t *testing.T) {
	limiter := initRateLimiter(nil)
	if _, ok := limiter.(*ratelimit.MemoryLimiter); !ok {
		t.Fatalf("expected memory limiter, got %T", limiter)
	}
}
