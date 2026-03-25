package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"i8sl/internal/code"
	"i8sl/internal/config"
	"i8sl/internal/ratelimit"
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
	ShortURL      string `json:"short_url"`
	URL           string `json:"url"`
	IsExpired     bool   `json:"is_expired"`
	ExpiredReason string `json:"expired_reason"`
}

func TestDeleteRuleRequiresAdminTokenWhenConfigured(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
		AdminToken:              "super-secret",
	}, time.Now)
	defer cleanup()

	rule := createRule(t, ts, map[string]any{
		"url": "https://example.com/protected-delete",
	})

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/rules/"+rule.Code, nil)
	if err != nil {
		t.Fatalf("build delete request: %v", err)
	}

	deleteRes, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete without auth: %v", err)
	}
	defer deleteRes.Body.Close()

	if deleteRes.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(deleteRes.Body)
		t.Fatalf("expected 401, got %d: %s", deleteRes.StatusCode, string(body))
	}

	deleteReq, err = http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/rules/"+rule.Code, nil)
	if err != nil {
		t.Fatalf("build authenticated delete request: %v", err)
	}
	deleteReq.Header.Set("Authorization", "Bearer super-secret")

	deleteRes, err = http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete with auth: %v", err)
	}
	defer deleteRes.Body.Close()

	if deleteRes.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(deleteRes.Body)
		t.Fatalf("expected 204, got %d: %s", deleteRes.StatusCode, string(body))
	}
}

func TestMetricsEndpoint(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now)
	defer cleanup()

	res, err := http.Get(ts.URL + "/health/live")
	if err != nil {
		t.Fatalf("call health endpoint: %v", err)
	}
	res.Body.Close()

	metricsRes, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("call metrics endpoint: %v", err)
	}
	defer metricsRes.Body.Close()

	if metricsRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(metricsRes.Body)
		t.Fatalf("expected 200, got %d: %s", metricsRes.StatusCode, string(body))
	}

	body, err := io.ReadAll(metricsRes.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}

	content := string(body)
	if !strings.Contains(content, "i8sl_http_requests_total") || !strings.Contains(content, "i8sl_admin_auth_failures_total") {
		t.Fatalf("expected key metrics in output, got: %s", content)
	}
}

func TestRequestIDEchoedInResponse(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now)
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/health/live", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Request-ID", "demo-request-id")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer res.Body.Close()

	if got := res.Header.Get("X-Request-ID"); got != "demo-request-id" {
		t.Fatalf("expected echoed request id, got %q", got)
	}
}

func TestTrustedForwardedHeadersAffectShortURL(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
		TrustedProxies:          mustPrefixes(t, "127.0.0.1/32", "::1/128"),
	}, time.Now)
	defer cleanup()

	body, err := json.Marshal(map[string]any{"url": "https://example.com/forwarded"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/rules", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "short.example.com")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 201, got %d: %s", res.StatusCode, string(body))
	}

	var rule rulePayload
	if err := json.NewDecoder(res.Body).Decode(&rule); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !strings.HasPrefix(rule.ShortURL, "https://short.example.com/r/") {
		t.Fatalf("expected forwarded host in short_url, got %q", rule.ShortURL)
	}
}

func TestRejectPrivateTargetsWhenEnabled(t *testing.T) {
	ts, cleanup := newTestServer(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
		RejectPrivateTargets:    true,
	}, time.Now)
	defer cleanup()

	res := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/rules", map[string]any{
		"url": "http://localhost:8080/internal",
	})
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, string(body))
	}
}

func TestRateLimiterBackendUnavailableReturns503(t *testing.T) {
	ts, cleanup := newTestServerWithLimiter(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now, stubLimiter{allowErr: errors.New("redis unavailable")})
	defer cleanup()

	res := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/rules", map[string]any{
		"url": "https://example.com/limiter-unavailable",
	})
	defer res.Body.Close()

	if res.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 503, got %d: %s", res.StatusCode, string(body))
	}
}

func TestReadinessFailsWhenRateLimiterUnavailable(t *testing.T) {
	ts, cleanup := newTestServerWithLimiter(t, config.Config{
		ServiceName:             "I8SL",
		CodeLength:              6,
		GenerationRatePerMinute: 60,
		GenerationBurst:         10,
	}, time.Now, stubLimiter{pingErr: errors.New("redis unavailable")})
	defer cleanup()

	res, err := http.Get(ts.URL + "/health/ready")
	if err != nil {
		t.Fatalf("call readiness: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 503, got %d: %s", res.StatusCode, string(body))
	}
}

func newTestServer(t *testing.T, cfg config.Config, now func() time.Time) (*httptest.Server, func()) {
	t.Helper()

	return newTestServerWithLimiter(t, cfg, now, ratelimit.NewMemory(cfg.GenerationRatePerMinute, cfg.GenerationBurst, 10*time.Minute))
}

func newTestServerWithLimiter(t *testing.T, cfg config.Config, now func() time.Time, limiter ratelimit.Limiter) (*httptest.Server, func()) {
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
	if cfg.RejectPrivateTargets {
		service = shortener.NewService(
			store,
			code.NewGenerator(cfg.CodeLength),
			cfg.BaseURL,
			shortener.WithNow(now),
			shortener.WithPrivateTargetRejection(true),
		)
	}
	logger := slog.New(slog.NewTextHandler(testLogWriter{t: t}, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	}))
	handler := server.NewHandler(cfg, logger, service, limiter)

	ts := httptest.NewServer(handler)

	return ts, func() {
		ts.Close()
		_ = store.Close()
	}
}

func newTestServerWithComponents(t *testing.T, cfg config.Config, service *shortener.Service, limiter ratelimit.Limiter) (*httptest.Server, func()) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(testLogWriter{t: t}, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	}))
	handler := server.NewHandler(cfg, logger, service, limiter)
	ts := httptest.NewServer(handler)

	return ts, func() {
		ts.Close()
		_ = limiter.Close()
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

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()

	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			t.Fatalf("parse prefix %q: %v", value, err)
		}

		prefixes = append(prefixes, prefix)
	}

	return prefixes
}

type stubLimiter struct {
	allowErr error
	pingErr  error
}

func (s stubLimiter) Allow(context.Context, string) (bool, error) {
	if s.allowErr != nil {
		return false, s.allowErr
	}

	return true, nil
}

func (s stubLimiter) Ping(context.Context) error {
	return s.pingErr
}

func (s stubLimiter) Close() error {
	return nil
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
