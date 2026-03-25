package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"i8sl/internal/code"
	"i8sl/internal/config"
	"i8sl/internal/ratelimit"
	"i8sl/internal/server"
	"i8sl/internal/shortener"
	"i8sl/internal/storage/postgres"
)

func TestPostgresIntegrationCreateInspectAndDeleteRule(t *testing.T) {
	dsn := os.Getenv("I8SL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("I8SL_TEST_POSTGRES_DSN is not set")
	}

	store, err := postgres.NewStore(dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	cfg := config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
		StorageDriver:           "postgres",
		DBURI:                   dsn,
	}

	service := shortener.NewService(store, code.NewGenerator(cfg.CodeLength), cfg.BaseURL)
	ts := newPostgresTestServer(t, cfg, service)
	defer ts.Close()

	alias := "pg" + time.Now().UTC().Format("150405")
	created := createRule(t, ts, map[string]any{
		"url":   "https://example.com/postgres-runtime",
		"alias": alias,
	})

	if created.Code != alias {
		t.Fatalf("expected alias %q, got %q", alias, created.Code)
	}

	res, err := http.Get(ts.URL + "/api/v1/rules/" + alias)
	if err != nil {
		t.Fatalf("get postgres-backed rule: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body := readResponseBody(t, res)
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, body)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/rules/"+alias, nil)
	if err != nil {
		t.Fatalf("build delete request: %v", err)
	}

	deleteRes, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete postgres-backed rule: %v", err)
	}
	defer deleteRes.Body.Close()

	if deleteRes.StatusCode != http.StatusNoContent {
		body := readResponseBody(t, deleteRes)
		t.Fatalf("expected 204, got %d: %s", deleteRes.StatusCode, body)
	}
}

func newPostgresTestServer(t *testing.T, cfg config.Config, service *shortener.Service) *httptest.Server {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(testLogWriter{t: t}, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	}))

	return httptest.NewServer(server.NewHandler(cfg, logger, service, ratelimit.NewMemory(cfg.GenerationRatePerMinute, cfg.GenerationBurst, 10*time.Minute)))
}

func readResponseBody(t *testing.T, res *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	return string(body)
}
