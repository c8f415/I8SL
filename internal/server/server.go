package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"i8sl/docs"
	"i8sl/internal/config"
	"i8sl/internal/ratelimit"
	"i8sl/internal/shortener"
)

const jsonBodyLimit = 1 << 20

type API struct {
	cfg     config.Config
	logger  *slog.Logger
	service *shortener.Service
	limiter *ratelimit.Limiter
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
	Status  string            `json:"status"`
	Service string            `json:"service"`
	Time    time.Time         `json:"time"`
	Checks  map[string]string `json:"checks,omitempty"`
}

func NewHandler(cfg config.Config, logger *slog.Logger, service *shortener.Service) http.Handler {
	api := &API{
		cfg:     cfg,
		logger:  logger,
		service: service,
		limiter: ratelimit.New(cfg.GenerationRatePerMinute, cfg.GenerationBurst, 10*time.Minute),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", api.handleLanding)
	mux.HandleFunc("GET /docs", api.handleDocs)
	mux.HandleFunc("GET /openapi.yaml", api.handleOpenAPI)
	mux.HandleFunc("GET /health/live", api.handleLive)
	mux.HandleFunc("GET /health/ready", api.handleReady)
	mux.HandleFunc("GET /api/v1/generate", api.withRateLimit(api.handleGenerate))
	mux.HandleFunc("POST /api/v1/rules", api.withRateLimit(api.handleCreateRule))
	mux.HandleFunc("GET /api/v1/rules/{code}", api.handleGetRule)
	mux.HandleFunc("DELETE /api/v1/rules/{code}", api.handleDeleteRule)
	mux.HandleFunc("GET /r/{code}", api.handleRedirect)

	return api.withLogging(mux)
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
	_, _ = w.Write(docs.OpenAPI)
}

func (a *API) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Service: a.cfg.ServiceName,
		Time:    time.Now().UTC(),
		Checks: map[string]string{
			"process": "alive",
		},
	})
}

func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := a.service.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{
			Status:  "degraded",
			Service: a.cfg.ServiceName,
			Time:    time.Now().UTC(),
			Checks: map[string]string{
				"store": err.Error(),
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Service: a.cfg.ServiceName,
		Time:    time.Now().UTC(),
		Checks: map[string]string{
			"store": "reachable",
		},
	})
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
		a.writeServiceError(w, r, err)
		return
	}

	http.Redirect(w, r, rule.URL, http.StatusFound)
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
		a.logger.Error("request failed", "path", r.URL.Path, "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (a *API) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.limiter.Allow(clientIP(r)) {
			writeProblem(w, http.StatusTooManyRequests, "rate_limited", "generation rate limit exceeded")
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

	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}

	return scheme + "://" + host
}

func (a *API) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		a.logger.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(startedAt).String(),
			"client_ip", clientIP(r),
		)
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

func clientIP(r *http.Request) string {
	forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if forwarded != "" {
		return forwarded
	}

	realIP := strings.TrimSpace(r.Header.Get("X-Real-Ip"))
	if realIP != "" {
		return realIP
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}

	return r.RemoteAddr
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
