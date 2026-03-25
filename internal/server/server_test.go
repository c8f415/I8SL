package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"i8sl/internal/code"
	"i8sl/internal/config"
	"i8sl/internal/server"
	"i8sl/internal/shortener"
	"i8sl/internal/storage/sqlite"
)

func TestCreateRuleAndRedirectUntilUsageLimit(t *testing.T) {
	baseTime := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	now := baseTime

	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, func() time.Time { return now })
	defer cleanup()

	created := createRule(t, ts, map[string]any{
		"url":        "https://example.com/articles/usage-limits",
		"max_usages": 2,
	})

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	for i := 0; i < 2; i++ {
		res, err := client.Get(ts.URL + "/r/" + created.Code)
		if err != nil {
			t.Fatalf("resolve rule: %v", err)
		}

		if res.StatusCode != http.StatusFound {
			t.Fatalf("expected 302, got %d", res.StatusCode)
		}

		if got := res.Header.Get("Location"); got != created.URL {
			t.Fatalf("expected location %q, got %q", created.URL, got)
		}
		res.Body.Close()
	}

	res, err := client.Get(ts.URL + "/r/" + created.Code)
	if err != nil {
		t.Fatalf("resolve exhausted rule: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 after usage limit, got %d", res.StatusCode)
	}
}

func TestRuleExpiresByTime(t *testing.T) {
	baseTime := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	now := baseTime

	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, func() time.Time { return now })
	defer cleanup()

	created := createRule(t, ts, map[string]any{
		"url":         "https://example.com/articles/ttl",
		"ttl_seconds": 30,
	})

	now = now.Add(31 * time.Second)

	res, err := http.Get(ts.URL + "/api/v1/rules/" + created.Code)
	if err != nil {
		t.Fatalf("get rule metadata: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var rule rulePayload
	if err := json.NewDecoder(res.Body).Decode(&rule); err != nil {
		t.Fatalf("decode rule metadata: %v", err)
	}

	if !rule.IsExpired || rule.ExpiredReason != "ttl" {
		t.Fatalf("expected ttl expiration, got expired=%v reason=%q", rule.IsExpired, rule.ExpiredReason)
	}

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	redirectRes, err := client.Get(ts.URL + "/r/" + created.Code)
	if err != nil {
		t.Fatalf("resolve expired rule: %v", err)
	}
	defer redirectRes.Body.Close()

	if redirectRes.StatusCode != http.StatusGone {
		t.Fatalf("expected 410, got %d", redirectRes.StatusCode)
	}
}

func TestGenerationRateLimit(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 1,
		GenerationBurst:         1,
	}, time.Now)
	defer cleanup()

	_ = createRule(t, ts, map[string]any{
		"url": "https://example.com/articles/first",
	})

	res := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/rules", map[string]any{
		"url": "https://example.com/articles/second",
	})
	defer res.Body.Close()

	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", res.StatusCode)
	}
}

func TestGenerateEndpointAndDeleteRule(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now)
	defer cleanup()

	res, err := http.Get(ts.URL + "/api/v1/generate?url=https://example.com/query&alias=demo42")
	if err != nil {
		t.Fatalf("call generate endpoint: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 201, got %d: %s", res.StatusCode, string(body))
	}

	var rule rulePayload
	if err := json.NewDecoder(res.Body).Decode(&rule); err != nil {
		t.Fatalf("decode generate response: %v", err)
	}

	if rule.Code != "demo42" {
		t.Fatalf("expected alias demo42, got %q", rule.Code)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/rules/demo42", nil)
	if err != nil {
		t.Fatalf("build delete request: %v", err)
	}

	deleteRes, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	defer deleteRes.Body.Close()

	if deleteRes.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(deleteRes.Body)
		t.Fatalf("expected 204, got %d: %s", deleteRes.StatusCode, string(body))
	}

	getRes, err := http.Get(ts.URL + "/api/v1/rules/demo42")
	if err != nil {
		t.Fatalf("get deleted rule: %v", err)
	}
	defer getRes.Body.Close()

	if getRes.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(getRes.Body)
		t.Fatalf("expected 404, got %d: %s", getRes.StatusCode, string(body))
	}
}

func TestHealthEndpoints(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now)
	defer cleanup()

	for _, path := range []string{"/health/live", "/health/ready"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("call %s: %v", path, err)
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("expected 200 for %s, got %d: %s", path, res.StatusCode, string(body))
		}
	}
}

func TestDocsEndpoints(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now)
	defer cleanup()

	for _, path := range []string{"/", "/docs", "/openapi.yaml"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("call %s: %v", path, err)
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("expected 200 for %s, got %d: %s", path, res.StatusCode, string(body))
		}
	}
}

func TestCustomAliasConflict(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now)
	defer cleanup()

	_ = createRule(t, ts, map[string]any{
		"url":   "https://example.com/first",
		"alias": "alias01",
	})

	res := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/rules", map[string]any{
		"url":   "https://example.com/second",
		"alias": "alias01",
	})
	defer res.Body.Close()

	if res.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 409, got %d: %s", res.StatusCode, string(body))
	}
}

type rulePayload struct {
	Code          string `json:"code"`
	URL           string `json:"url"`
	IsExpired     bool   `json:"is_expired"`
	ExpiredReason string `json:"expired_reason"`
}

func newTestServer(t *testing.T, cfg config.Config, now func() time.Time) (*httptest.Server, func()) {
	t.Helper()

	if cfg.CodeLength == 0 {
		cfg.CodeLength = 6
	}

	if cfg.GenerationRatePerMinute == 0 {
		cfg.GenerationRatePerMinute = 60
	}

	if cfg.GenerationBurst == 0 {
		cfg.GenerationBurst = 10
	}

	if cfg.ServiceName == "" {
		cfg.ServiceName = "I8SL"
	}

	store, err := sqlite.NewStore(filepath.Join(t.TempDir(), "i8sl-test.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	service := shortener.NewService(store, code.NewGenerator(cfg.CodeLength), cfg.BaseURL, shortener.WithNow(now))
	logger := slog.New(slog.NewTextHandler(testLogWriter{t: t}, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	}))
	handler := server.NewHandler(cfg, logger, service)

	ts := httptest.NewServer(handler)

	return ts, func() {
		ts.Close()
		_ = store.Close()
	}
}

func createRule(t *testing.T, ts *httptest.Server, payload map[string]any) rulePayload {
	t.Helper()

	res := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/rules", payload)
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 201, got %d: %s", res.StatusCode, string(body))
	}

	var rule rulePayload
	if err := json.NewDecoder(res.Body).Decode(&rule); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	return rule
}

func doJSONRequest(t *testing.T, method, url string, payload map[string]any) *http.Response {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}

	return res
}

type testLogWriter struct {
	t *testing.T
}

func (w testLogWriter) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line != "" {
		w.t.Log(line)
	}

	return len(p), nil
}
