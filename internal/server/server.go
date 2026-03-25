package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	apispec "i8sl/api"
	"i8sl/docs"
	"i8sl/internal/config"
	"i8sl/internal/observability/buildinfo"
	"i8sl/internal/observability/metrics"
	"i8sl/internal/ratelimit"
	"i8sl/internal/shortener"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const jsonBodyLimit = 1 << 20

type contextKey string

const requestIDContextKey contextKey = "request_id"

type API struct {
	cfg     config.Config
	logger  *slog.Logger
	service *shortener.Service
	limiter ratelimit.Limiter
	metrics *metrics.Metrics
}

type createRuleRequest struct {
	URL        string `json:"url"`
	Alias      string `json:"alias,omitempty"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
	MaxUsages  int64  `json:"max_usages,omitempty"`
}

type ruleResponse struct {
	Code          string     `json:"code"`
	ShortURL      string     `json:"short_url"`
	URL           string     `json:"url"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	MaxUsages     *int64     `json:"max_usages,omitempty"`
	UsedCount     int64      `json:"used_count"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	IsExpired     bool       `json:"is_expired"`
	ExpiredReason string     `json:"expired_reason,omitempty"`
}

type problemResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type healthResponse struct {
	Status      string            `json:"status"`
	Service     string            `json:"service"`
	Environment string            `json:"environment"`
	Version     string            `json:"version"`
	Commit      string            `json:"commit"`
	BuildTime   string            `json:"build_time"`
	Time        time.Time         `json:"time"`
	Checks      map[string]string `json:"checks,omitempty"`
}

func NewHandler(cfg config.Config, logger *slog.Logger, service *shortener.Service, limiter ratelimit.Limiter) http.Handler {
	if cfg.MetricsPath == "" {
		cfg.MetricsPath = "/metrics"
	}
	if limiter == nil {
		limiter = ratelimit.NewMemory(cfg.GenerationRatePerMinute, cfg.GenerationBurst, 10*time.Minute)
	}

	apiMetrics := metrics.New()
	api := &API{
		cfg:     cfg,
		logger:  logger,
		service: service,
		limiter: limiter,
		metrics: apiMetrics,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", api.instrument("landing", api.handleLanding))
	mux.HandleFunc("GET /docs", api.instrument("docs", api.handleDocs))
	mux.HandleFunc("GET /openapi.yaml", api.instrument("openapi", api.handleOpenAPI))
	mux.HandleFunc("GET /health/live", api.instrument("health_live", api.handleLive))
	mux.HandleFunc("GET /health/ready", api.instrument("health_ready", api.handleReady))
	mux.HandleFunc("GET /api/v1/generate", api.instrument("generate_query", api.withRateLimit("generate_query", api.handleGenerate)))
	mux.HandleFunc("POST /api/v1/rules", api.instrument("create_rule", api.withRateLimit("create_rule", api.handleCreateRule)))
	mux.HandleFunc("GET /api/v1/rules/{code}", api.instrument("get_rule", api.handleGetRule))
	mux.HandleFunc("DELETE /api/v1/rules/{code}", api.instrument("delete_rule", api.withAdminAuth(api.handleDeleteRule)))
	mux.HandleFunc("GET /r/{code}", api.instrument("redirect", api.handleRedirect))
	mux.Handle("GET "+cfg.MetricsPath, api.instrumentHandler("metrics", apiMetrics.Handler()))

	return api.withRequestID(api.withTracing(api.withRecovery(api.withLogging(mux))))
}

func (a *API) handleLanding(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(docs.LandingHTML)
}

func (a *API) handleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(docs.ScalarHTML)
}

func (a *API) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(apispec.OpenAPI)
}

func (a *API) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.healthResponse("ok", map[string]string{"process": "alive"}))
}

func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := a.service.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, a.healthResponse("degraded", map[string]string{"store": err.Error()}))
		return
	}

	if err := a.limiter.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, a.healthResponse("degraded", map[string]string{"rate_limiter": err.Error()}))
		return
	}

	writeJSON(w, http.StatusOK, a.healthResponse("ok", map[string]string{"store": "reachable", "rate_limiter": "reachable"}))
}

func (a *API) handleGenerate(w http.ResponseWriter, r *http.Request) {
	req, err := requestFromQuery(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	a.createRule(r.Context(), w, r, req)
}

func (a *API) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req createRuleRequest

	body := http.MaxBytesReader(w, r.Body, jsonBodyLimit)
	defer body.Close()

	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_json", decodeJSONError(err))
		return
	}

	if err := decoder.Decode(new(struct{})); err != io.EOF {
		writeProblem(w, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON object")
		return
	}

	a.createRule(r.Context(), w, r, req)
}

func (a *API) createRule(ctx context.Context, w http.ResponseWriter, r *http.Request, req createRuleRequest) {
	rule, err := a.service.CreateRule(ctx, shortener.CreateInput{
		URL:        req.URL,
		Alias:      req.Alias,
		TTLSeconds: req.TTLSeconds,
		MaxUsages:  req.MaxUsages,
	})
	if err != nil {
		a.writeServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, a.ruleResponse(r, rule))
}

func (a *API) handleGetRule(w http.ResponseWriter, r *http.Request) {
	rule, err := a.service.GetRule(r.Context(), r.PathValue("code"))
	if err != nil {
		a.writeServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, a.ruleResponse(r, rule))
}

func (a *API) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if err := a.service.DeleteRule(r.Context(), r.PathValue("code")); err != nil {
		a.writeServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRedirect(w http.ResponseWriter, r *http.Request) {
	rule, err := a.service.ResolveRule(r.Context(), r.PathValue("code"))
	if err != nil {
		var expiredErr *shortener.ExpiredError

		switch {
		case errors.As(err, &expiredErr):
			a.metrics.IncRedirect("expired")
		case errors.Is(err, shortener.ErrNotFound):
			a.metrics.IncRedirect("not_found")
		default:
			a.metrics.IncRedirect("error")
		}

		a.writeServiceError(w, r, err)
		return
	}

	a.metrics.IncRedirect("success")
	http.Redirect(w, r, rule.URL, http.StatusFound)
}

func (a *API) healthResponse(status string, checks map[string]string) healthResponse {
	info := buildinfo.Current()

	return healthResponse{
		Status:      status,
		Service:     a.cfg.ServiceName,
		Environment: a.cfg.Environment,
		Version:     info.Version,
		Commit:      info.Commit,
		BuildTime:   info.BuildTime,
		Time:        time.Now().UTC(),
		Checks:      checks,
	}
}

func (a *API) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var validationErr *shortener.ValidationError
	var expiredErr *shortener.ExpiredError

	switch {
	case errors.As(err, &validationErr):
		writeProblem(w, http.StatusBadRequest, "validation_error", validationErr.Error())
	case errors.As(err, &expiredErr):
		writeProblem(w, http.StatusGone, "rule_expired", fmt.Sprintf("rule %q expired because %s", expiredErr.Rule.Code, expiredErr.Reason))
	case errors.Is(err, shortener.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "rule not found")
	case errors.Is(err, shortener.ErrAlreadyExists):
		writeProblem(w, http.StatusConflict, "already_exists", "rule alias already exists")
	default:
		a.logger.Error(
			"request failed",
			"path", r.URL.Path,
			"request_id", requestIDFromContext(r.Context()),
			"client_ip", a.clientIP(r),
			"error", err,
		)
		writeProblem(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (a *API) withRateLimit(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed, err := a.limiter.Allow(r.Context(), a.clientIP(r))
		if err != nil {
			a.logger.Error("rate limiter failed", "request_id", requestIDFromContext(r.Context()), "error", err)
			writeProblem(w, http.StatusServiceUnavailable, "rate_limiter_unavailable", "rate limiter backend is unavailable")
			return
		}

		if !allowed {
			a.metrics.IncRateLimited(route)
			writeProblem(w, http.StatusTooManyRequests, "rate_limited", "generation rate limit exceeded")
			return
		}

		next(w, r)
	}
}

func (a *API) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	if a.cfg.AdminToken == "" {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			token = strings.TrimSpace(r.Header.Get("X-API-Key"))
		}

		if subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.AdminToken)) != 1 {
			a.metrics.IncAdminAuthFailure()
			w.Header().Set("WWW-Authenticate", `Bearer realm="i8sl-admin"`)
			writeProblem(w, http.StatusUnauthorized, "admin_auth_required", "valid admin token is required")
			return
		}

		next(w, r)
	}
}

func (a *API) ruleResponse(r *http.Request, rule shortener.Rule) ruleResponse {
	isExpired, reason := rule.Expired(a.service.Now())

	return ruleResponse{
		Code:          rule.Code,
		ShortURL:      shortener.BuildShortURL(a.publicBaseURL(r), rule.Code),
		URL:           rule.URL,
		CreatedAt:     rule.CreatedAt,
		ExpiresAt:     rule.ExpiresAt,
		MaxUsages:     rule.MaxUsages,
		UsedCount:     rule.UsedCount,
		LastUsedAt:    rule.LastUsedAt,
		IsExpired:     isExpired,
		ExpiredReason: reason,
	}
}

func (a *API) publicBaseURL(r *http.Request) string {
	if a.cfg.BaseURL != "" {
		return a.cfg.BaseURL
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host

	if a.forwardedHeadersTrusted(r) {
		if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
			scheme = forwardedProto
		}

		if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
			host = forwardedHost
		}
	}

	return scheme + "://" + host
}

func (a *API) instrument(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(recorder, r)
		a.metrics.ObserveHTTPRequest(route, r.Method, recorder.status, time.Since(startedAt))
	}
}

func (a *API) instrumentHandler(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		a.metrics.ObserveHTTPRequest(route, r.Method, recorder.status, time.Since(startedAt))
	})
}

func (a *API) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := sanitizeRequestID(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}

		ctx := context.WithValue(r.Context(), requestIDContextKey, requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		a.logger.Info(
			"request completed",
			"request_id", requestIDFromContext(r.Context()),
			"trace_id", traceIDFromContext(r.Context()),
			"span_id", spanIDFromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(startedAt).String(),
			"client_ip", a.clientIP(r),
			"user_agent", r.UserAgent(),
		)
	})
}

func (a *API) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				span := trace.SpanFromContext(r.Context())
				span.RecordError(fmt.Errorf("panic: %v", recovered))
				span.SetStatus(codes.Error, "panic recovered")

				a.logger.Error(
					"panic recovered",
					"request_id", requestIDFromContext(r.Context()),
					"path", r.URL.Path,
					"client_ip", a.clientIP(r),
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				writeProblem(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func (a *API) withTracing(next http.Handler) http.Handler {
	tracer := otel.Tracer("i8sl/http")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path)
		defer span.End()

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r.WithContext(ctx))

		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", r.URL.Path),
			attribute.String("http.request_id", requestIDFromContext(ctx)),
			attribute.Int("http.status_code", recorder.status),
		)
		if recorder.status >= http.StatusBadRequest {
			span.SetStatus(codes.Error, http.StatusText(recorder.status))
		}
	})
}

func requestFromQuery(r *http.Request) (createRuleRequest, error) {
	q := r.URL.Query()
	req := createRuleRequest{
		URL:   strings.TrimSpace(q.Get("url")),
		Alias: strings.TrimSpace(q.Get("alias")),
	}

	if ttl := strings.TrimSpace(q.Get("ttl_seconds")); ttl != "" {
		value, err := strconv.ParseInt(ttl, 10, 64)
		if err != nil {
			return createRuleRequest{}, fmt.Errorf("ttl_seconds must be an integer")
		}

		req.TTLSeconds = value
	}

	if usages := strings.TrimSpace(q.Get("max_usages")); usages != "" {
		value, err := strconv.ParseInt(usages, 10, 64)
		if err != nil {
			return createRuleRequest{}, fmt.Errorf("max_usages must be an integer")
		}

		req.MaxUsages = value
	}

	return req, nil
}

func writeProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, problemResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func decodeJSONError(err error) string {
	if errors.Is(err, io.EOF) {
		return "request body is required"
	}

	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &syntaxErr):
		return fmt.Sprintf("invalid JSON at byte %d", syntaxErr.Offset)
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return fmt.Sprintf("field %q has the wrong type", typeErr.Field)
		}
		return "request body contains an invalid value"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return strings.TrimPrefix(err.Error(), "json: ")
	default:
		return err.Error()
	}
}

func (a *API) clientIP(r *http.Request) string {
	if a.forwardedHeadersTrusted(r) {
		forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
		if forwarded != "" {
			return forwarded
		}

		realIP := strings.TrimSpace(r.Header.Get("X-Real-Ip"))
		if realIP != "" {
			return realIP
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}

	return r.RemoteAddr
}

func (a *API) forwardedHeadersTrusted(r *http.Request) bool {
	if len(a.cfg.TrustedProxies) == 0 {
		return false
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}

	for _, prefix := range a.cfg.TrustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

func sanitizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if len(value) > 128 {
		value = value[:128]
	}

	return value
}

func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}

	return strconv.FormatInt(time.Now().UnixNano(), 16)
}

func traceIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}

	return spanContext.TraceID().String()
}

func spanIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}

	return spanContext.SpanID().String()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
