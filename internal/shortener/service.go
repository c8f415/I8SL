package shortener

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"i8sl/internal/code"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var (
	ErrNotFound      = errors.New("rule not found")
	ErrAlreadyExists = errors.New("rule already exists")

	aliasPattern = regexp.MustCompile(`^[A-Za-z0-9]{4,32}$`)
)

type Rule struct {
	Code       string
	URL        string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	MaxUsages  *int64
	UsedCount  int64
	LastUsedAt *time.Time
}

type CreateInput struct {
	URL        string
	Alias      string
	TTLSeconds int64
	MaxUsages  int64
}

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}

	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

type ExpiredError struct {
	Rule   Rule
	Reason string
}

func (e *ExpiredError) Error() string {
	return fmt.Sprintf("rule expired because %s", e.Reason)
}

type Store interface {
	Ping(context.Context) error
	Close() error
	CreateRule(context.Context, Rule) (Rule, error)
	GetRule(context.Context, string) (Rule, error)
	DeleteRule(context.Context, string) error
	DeleteExpired(context.Context, time.Time) (int64, error)
	ResolveRule(context.Context, string, time.Time) (Rule, error)
}

type Service struct {
	store                Store
	generator            code.Generator
	baseURL              string
	rejectPrivateTargets bool
	now                  func() time.Time
}

type Option func(*Service)

func WithNow(now func() time.Time) Option {
	return func(s *Service) {
		s.now = now
	}
}

func WithPrivateTargetRejection(enabled bool) Option {
	return func(s *Service) {
		s.rejectPrivateTargets = enabled
	}
}

func NewService(store Store, generator code.Generator, baseURL string, opts ...Option) *Service {
	svc := &Service{
		store:     store,
		generator: generator,
		baseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		now: func() time.Time {
			return time.Now().UTC()
		},
	}

	for _, opt := range opts {
		opt(svc)
	}

	return svc
}

func (s *Service) Now() time.Time {
	return s.now().UTC()
}

func (s *Service) Ping(ctx context.Context) error {
	ctx, span := otel.Tracer("i8sl/shortener").Start(ctx, "service.ping")
	defer span.End()

	return s.store.Ping(ctx)
}

func (s *Service) DeleteExpired(ctx context.Context) (int64, error) {
	ctx, span := otel.Tracer("i8sl/shortener").Start(ctx, "service.delete_expired")
	defer span.End()

	deleted, err := s.store.DeleteExpired(ctx, s.Now())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delete expired failed")
		return 0, err
	}

	span.SetAttributes(attribute.Int64("rules.deleted", deleted))
	return deleted, nil
}

func (s *Service) CreateRule(ctx context.Context, input CreateInput) (Rule, error) {
	ctx, span := otel.Tracer("i8sl/shortener").Start(ctx, "service.create_rule")
	defer span.End()
	span.SetAttributes(
		attribute.Bool("rule.has_alias", strings.TrimSpace(input.Alias) != ""),
		attribute.Int64("rule.ttl_seconds", input.TTLSeconds),
		attribute.Int64("rule.max_usages", input.MaxUsages),
	)

	now := s.Now()
	rule, err := s.ruleFromInput(input, now)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid create input")
		return Rule{}, err
	}

	if rule.Code != "" {
		created, err := s.store.CreateRule(ctx, rule)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "create rule failed")
			return Rule{}, err
		}

		span.SetAttributes(attribute.String("rule.code", created.Code))
		return created, nil
	}

	for range 10 {
		code, err := s.generator.Generate()
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "generate code failed")
			return Rule{}, err
		}

		rule.Code = code
		created, err := s.store.CreateRule(ctx, rule)
		if errors.Is(err, ErrAlreadyExists) {
			continue
		}

		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "create generated rule failed")
			return Rule{}, err
		}

		span.SetAttributes(attribute.String("rule.code", created.Code))
		return created, nil
	}

	err = fmt.Errorf("could not generate a unique short code")
	span.RecordError(err)
	span.SetStatus(codes.Error, "code collision exhaustion")
	return Rule{}, err
}

func (s *Service) GetRule(ctx context.Context, code string) (Rule, error) {
	ctx, span := otel.Tracer("i8sl/shortener").Start(ctx, "service.get_rule")
	defer span.End()

	code, err := validateCode(code)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid code")
		return Rule{}, err
	}
	span.SetAttributes(attribute.String("rule.code", code))

	rule, err := s.store.GetRule(ctx, code)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "get rule failed")
		return Rule{}, err
	}

	return rule, nil
}

func (s *Service) DeleteRule(ctx context.Context, code string) error {
	ctx, span := otel.Tracer("i8sl/shortener").Start(ctx, "service.delete_rule")
	defer span.End()

	code, err := validateCode(code)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid code")
		return err
	}
	span.SetAttributes(attribute.String("rule.code", code))

	err = s.store.DeleteRule(ctx, code)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delete rule failed")
	}

	return err
}

func (s *Service) ResolveRule(ctx context.Context, code string) (Rule, error) {
	ctx, span := otel.Tracer("i8sl/shortener").Start(ctx, "service.resolve_rule")
	defer span.End()

	code, err := validateCode(code)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid code")
		return Rule{}, err
	}
	span.SetAttributes(attribute.String("rule.code", code))

	rule, err := s.store.ResolveRule(ctx, code, s.Now())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "resolve rule failed")
		return Rule{}, err
	}

	return rule, nil
}

func (s *Service) ruleFromInput(input CreateInput, now time.Time) (Rule, error) {
	target, err := validateTargetURL(input.URL, s.rejectPrivateTargets)
	if err != nil {
		return Rule{}, err
	}

	alias, err := normalizeAlias(input.Alias)
	if err != nil {
		return Rule{}, err
	}

	var expiresAt *time.Time
	if input.TTLSeconds < 0 {
		return Rule{}, &ValidationError{Field: "ttl_seconds", Message: "must be greater than or equal to 0"}
	}

	if input.TTLSeconds > 0 {
		expiry := now.Add(time.Duration(input.TTLSeconds) * time.Second)
		expiresAt = &expiry
	}

	var maxUsages *int64
	if input.MaxUsages < 0 {
		return Rule{}, &ValidationError{Field: "max_usages", Message: "must be greater than or equal to 0"}
	}

	if input.MaxUsages > 0 {
		value := input.MaxUsages
		maxUsages = &value
	}

	return Rule{
		Code:      alias,
		URL:       target.String(),
		CreatedAt: now,
		ExpiresAt: expiresAt,
		MaxUsages: maxUsages,
	}, nil
}

func validateTargetURL(raw string, rejectPrivateTargets bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, &ValidationError{Field: "url", Message: "is required"}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, &ValidationError{Field: "url", Message: "must be a valid absolute URL"}
	}

	if !parsed.IsAbs() || parsed.Host == "" {
		return nil, &ValidationError{Field: "url", Message: "must be a valid absolute URL"}
	}

	switch parsed.Scheme {
	case "http", "https":
		if rejectPrivateTargets {
			if err := validatePublicTarget(parsed); err != nil {
				return nil, err
			}
		}

		return parsed, nil
	default:
		return nil, &ValidationError{Field: "url", Message: "only http and https URLs are supported"}
	}
}

func validatePublicTarget(target *url.URL) error {
	hostname := strings.TrimSpace(strings.ToLower(target.Hostname()))
	if hostname == "" {
		return &ValidationError{Field: "url", Message: "must include a hostname"}
	}

	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") {
		return &ValidationError{Field: "url", Message: "private and local targets are not allowed"}
	}

	ip := net.ParseIP(hostname)
	if ip == nil {
		return nil
	}

	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return &ValidationError{Field: "url", Message: "private and local targets are not allowed"}
	}

	return nil
}

func normalizeAlias(raw string) (string, error) {
	alias := strings.TrimSpace(raw)
	if alias == "" {
		return "", nil
	}

	if !aliasPattern.MatchString(alias) {
		return "", &ValidationError{Field: "alias", Message: "must match ^[A-Za-z0-9]{4,32}$"}
	}

	return alias, nil
}

func validateCode(raw string) (string, error) {
	code := strings.TrimSpace(raw)
	if code == "" {
		return "", &ValidationError{Field: "code", Message: "is required"}
	}

	if !aliasPattern.MatchString(code) {
		return "", &ValidationError{Field: "code", Message: "must match ^[A-Za-z0-9]{4,32}$"}
	}

	return code, nil
}

func (r Rule) Expired(now time.Time) (bool, string) {
	now = now.UTC()

	if r.ExpiresAt != nil && !now.Before(r.ExpiresAt.UTC()) {
		return true, "ttl"
	}

	if r.MaxUsages != nil && r.UsedCount >= *r.MaxUsages {
		return true, "max_usages"
	}

	return false, ""
}

func BuildShortURL(baseURL, code string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "/r/" + code
	}

	return base + "/r/" + code
}
