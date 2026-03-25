package shortener

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"i8sl/internal/code"
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
	ResolveRule(context.Context, string, time.Time) (Rule, error)
}

type Service struct {
	store     Store
	generator code.Generator
	baseURL   string
	now       func() time.Time
}

type Option func(*Service)

func WithNow(now func() time.Time) Option {
	return func(s *Service) {
		s.now = now
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
	return s.store.Ping(ctx)
}

func (s *Service) CreateRule(ctx context.Context, input CreateInput) (Rule, error) {
	now := s.Now()
	rule, err := s.ruleFromInput(input, now)
	if err != nil {
		return Rule{}, err
	}

	if rule.Code != "" {
		created, err := s.store.CreateRule(ctx, rule)
		if err != nil {
			return Rule{}, err
		}

		return created, nil
	}

	for range 10 {
		code, err := s.generator.Generate()
		if err != nil {
			return Rule{}, err
		}

		rule.Code = code
		created, err := s.store.CreateRule(ctx, rule)
		if errors.Is(err, ErrAlreadyExists) {
			continue
		}

		if err != nil {
			return Rule{}, err
		}

		return created, nil
	}

	return Rule{}, fmt.Errorf("could not generate a unique short code")
}

func (s *Service) GetRule(ctx context.Context, code string) (Rule, error) {
	code, err := validateCode(code)
	if err != nil {
		return Rule{}, err
	}

	return s.store.GetRule(ctx, code)
}

func (s *Service) DeleteRule(ctx context.Context, code string) error {
	code, err := validateCode(code)
	if err != nil {
		return err
	}

	return s.store.DeleteRule(ctx, code)
}

func (s *Service) ResolveRule(ctx context.Context, code string) (Rule, error) {
	code, err := validateCode(code)
	if err != nil {
		return Rule{}, err
	}

	return s.store.ResolveRule(ctx, code, s.Now())
}

func (s *Service) ruleFromInput(input CreateInput, now time.Time) (Rule, error) {
	target, err := validateTargetURL(input.URL)
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

func validateTargetURL(raw string) (*url.URL, error) {
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
		return parsed, nil
	default:
		return nil, &ValidationError{Field: "url", Message: "only http and https URLs are supported"}
	}
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
