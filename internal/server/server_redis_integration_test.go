package server_test

import (
	"net/http"
	"os"
	"testing"
	"time"

	"i8sl/internal/code"
	"i8sl/internal/config"
	"i8sl/internal/ratelimit"
	"i8sl/internal/shortener"
	"i8sl/internal/storage/sqlite"
)

func TestRedisIntegrationRateLimitAndReadiness(t *testing.T) {
	redisAddr := os.Getenv("I8SL_TEST_REDIS_ADDR")
	if redisAddr == "" {
		t.Skip("I8SL_TEST_REDIS_ADDR is not set")
	}

	store, err := sqlite.NewStore(t.TempDir() + "/i8sl.db")
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer store.Close()

	limiter, err := ratelimit.NewRedis(redisAddr, "", 0, 1, 1, time.Minute, "i8sl:test:"+time.Now().UTC().Format("150405.000000000"))
	if err != nil {
		t.Fatalf("create redis limiter: %v", err)
	}
	defer limiter.Close()

	cfg := config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 1,
		GenerationBurst:         1,
		MetricsPath:             "/metrics",
	}

	service := shortener.NewService(store, code.NewGenerator(cfg.CodeLength), cfg.BaseURL)
	ts, cleanup := newTestServerWithComponents(t, cfg, service, limiter)
	defer cleanup()

	readyRes, err := http.Get(ts.URL + "/health/ready")
	if err != nil {
		t.Fatalf("call readiness: %v", err)
	}
	defer readyRes.Body.Close()

	if readyRes.StatusCode != http.StatusOK {
		body := readResponseBody(t, readyRes)
		t.Fatalf("expected 200, got %d: %s", readyRes.StatusCode, body)
	}

	first := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/rules", map[string]any{
		"url": "https://example.com/redis-1",
	})
	defer first.Body.Close()

	if first.StatusCode != http.StatusCreated {
		body := readResponseBody(t, first)
		t.Fatalf("expected 201, got %d: %s", first.StatusCode, body)
	}

	second := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/rules", map[string]any{
		"url": "https://example.com/redis-2",
	})
	defer second.Body.Close()

	if second.StatusCode != http.StatusTooManyRequests {
		body := readResponseBody(t, second)
		t.Fatalf("expected 429, got %d: %s", second.StatusCode, body)
	}
}
