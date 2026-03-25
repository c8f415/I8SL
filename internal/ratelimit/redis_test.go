package ratelimit

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

func TestRedisLimiterAllow(t *testing.T) {
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer server.Close()

	limiter, err := NewRedis(server.Addr(), "", 0, 60, 2, time.Minute, "test-limit")
	if err != nil {
		t.Fatalf("create redis limiter: %v", err)
	}
	defer limiter.Close()

	allowed, err := limiter.Allow(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("allow first request: %v", err)
	}
	if !allowed {
		t.Fatal("expected first request to be allowed")
	}

	allowed, err = limiter.Allow(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("allow second request: %v", err)
	}
	if !allowed {
		t.Fatal("expected second request to be allowed")
	}

	allowed, err = limiter.Allow(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("allow third request: %v", err)
	}
	if allowed {
		t.Fatal("expected third request to be rate limited")
	}
}

func TestRedisLimiterPing(t *testing.T) {
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer server.Close()

	limiter, err := NewRedis(server.Addr(), "", 0, 60, 1, time.Minute, "test-limit")
	if err != nil {
		t.Fatalf("create redis limiter: %v", err)
	}
	defer limiter.Close()

	if err := limiter.Ping(context.Background()); err != nil {
		t.Fatalf("ping redis limiter: %v", err)
	}
}
