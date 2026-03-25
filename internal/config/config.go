package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ServiceName             string
	Environment             string
	HTTPAddr                string
	BaseURL                 string
	StorageDriver           string
	SQLitePath              string
	DBURI                   string
	RateLimitBackend        string
	RedisAddr               string
	RedisPassword           string
	RedisDB                 int
	RedisKeyPrefix          string
	CodeLength              int
	GenerationRatePerMinute float64
	GenerationBurst         int
	AdminToken              string
	TrustedProxies          []netip.Prefix
	CleanupInterval         time.Duration
	MetricsPath             string
	RejectPrivateTargets    bool
	TracingEnabled          bool
	OTLPEndpoint            string
	OTLPInsecure            bool
	TraceSampleRatio        float64
	LogLevel                slog.Level
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
	IdleTimeout             time.Duration
	ShutdownTimeout         time.Duration
}

func Load() (Config, error) {
	codeLength, err := envInt("I8SL_CODE_LENGTH", 7)
	if err != nil {
		return Config{}, err
	}

	if codeLength < 4 || codeLength > 16 {
		return Config{}, fmt.Errorf("I8SL_CODE_LENGTH must be between 4 and 16")
	}

	ratePerMinute, err := envFloat("I8SL_GENERATION_RATE_PER_MINUTE", 30)
	if err != nil {
		return Config{}, err
	}

	if ratePerMinute <= 0 {
		return Config{}, fmt.Errorf("I8SL_GENERATION_RATE_PER_MINUTE must be greater than 0")
	}

	burst, err := envInt("I8SL_GENERATION_BURST", 10)
	if err != nil {
		return Config{}, err
	}

	if burst <= 0 {
		return Config{}, fmt.Errorf("I8SL_GENERATION_BURST must be greater than 0")
	}

	baseURL := strings.TrimSpace(os.Getenv("I8SL_BASE_URL"))
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL != "" {
		parsedBaseURL, err := url.Parse(baseURL)
		if err != nil || parsedBaseURL.Scheme == "" || parsedBaseURL.Host == "" {
			return Config{}, fmt.Errorf("I8SL_BASE_URL must be a valid absolute URL")
		}
	}

	storageDriver := strings.ToLower(envString("I8SL_STORAGE_DRIVER", "sqlite"))
	if storageDriver != "sqlite" && storageDriver != "postgres" {
		return Config{}, fmt.Errorf("I8SL_STORAGE_DRIVER must be either sqlite or postgres")
	}

	rateLimitBackend := strings.ToLower(envString("I8SL_RATE_LIMIT_BACKEND", "memory"))
	if rateLimitBackend != "memory" && rateLimitBackend != "redis" {
		return Config{}, fmt.Errorf("I8SL_RATE_LIMIT_BACKEND must be either memory or redis")
	}

	redisDB, err := envInt("I8SL_REDIS_DB", 0)
	if err != nil {
		return Config{}, err
	}

	if redisDB < 0 {
		return Config{}, fmt.Errorf("I8SL_REDIS_DB must be greater than or equal to 0")
	}

	trustedProxies, err := envPrefixes("I8SL_TRUSTED_PROXIES")
	if err != nil {
		return Config{}, err
	}

	cleanupInterval, err := envDuration("I8SL_CLEANUP_INTERVAL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}

	if cleanupInterval < 0 {
		return Config{}, fmt.Errorf("I8SL_CLEANUP_INTERVAL must be greater than or equal to 0")
	}

	metricsPath := envString("I8SL_METRICS_PATH", "/metrics")
	if !strings.HasPrefix(metricsPath, "/") {
		return Config{}, fmt.Errorf("I8SL_METRICS_PATH must start with '/'")
	}

	logLevel, err := envLogLevel("I8SL_LOG_LEVEL", slog.LevelInfo)
	if err != nil {
		return Config{}, err
	}

	traceSampleRatio, err := envFloat("I8SL_TRACE_SAMPLE_RATIO", 1)
	if err != nil {
		return Config{}, err
	}

	if traceSampleRatio < 0 || traceSampleRatio > 1 {
		return Config{}, fmt.Errorf("I8SL_TRACE_SAMPLE_RATIO must be between 0 and 1")
	}

	return Config{
		ServiceName:             envString("I8SL_SERVICE_NAME", "I8SL"),
		Environment:             envString("I8SL_ENV", "development"),
		HTTPAddr:                envString("I8SL_HTTP_ADDR", ":8080"),
		BaseURL:                 baseURL,
		StorageDriver:           storageDriver,
		SQLitePath:              envString("I8SL_SQLITE_PATH", "./i8sl.db"),
		DBURI:                   envString("I8SL_DB_URI", "postgres://i8sl:i8sl@localhost:5432/i8sl?sslmode=disable"),
		RateLimitBackend:        rateLimitBackend,
		RedisAddr:               envString("I8SL_REDIS_ADDR", "localhost:6379"),
		RedisPassword:           strings.TrimSpace(os.Getenv("I8SL_REDIS_PASSWORD")),
		RedisDB:                 redisDB,
		RedisKeyPrefix:          envString("I8SL_REDIS_KEY_PREFIX", "i8sl:rate_limit"),
		CodeLength:              codeLength,
		GenerationRatePerMinute: ratePerMinute,
		GenerationBurst:         burst,
		AdminToken:              strings.TrimSpace(os.Getenv("I8SL_ADMIN_TOKEN")),
		TrustedProxies:          trustedProxies,
		CleanupInterval:         cleanupInterval,
		MetricsPath:             metricsPath,
		RejectPrivateTargets:    envBool("I8SL_REJECT_PRIVATE_TARGETS", false),
		TracingEnabled:          envBool("I8SL_TRACING_ENABLED", false),
		OTLPEndpoint:            strings.TrimSpace(os.Getenv("I8SL_OTLP_ENDPOINT")),
		OTLPInsecure:            envBool("I8SL_OTLP_INSECURE", true),
		TraceSampleRatio:        traceSampleRatio,
		LogLevel:                logLevel,
		ReadTimeout:             5 * time.Second,
		WriteTimeout:            10 * time.Second,
		IdleTimeout:             60 * time.Second,
		ShutdownTimeout:         10 * time.Second,
	}, nil
}

func envString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	return value
}

func envInt(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}

	return parsed, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}

	return parsed, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}

	return parsed, nil
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}

	return parsed
}

func envLogLevel(key string, fallback slog.Level) (slog.Level, error) {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if value == "" {
		return fallback, nil
	}

	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("I8SL_LOG_LEVEL must be one of debug, info, warn, error")
	}
}

func envPrefixes(key string) ([]netip.Prefix, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))

	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}

		if addr, err := netip.ParseAddr(item); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}

		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", key, err)
		}

		prefixes = append(prefixes, prefix)
	}

	return prefixes, nil
}
