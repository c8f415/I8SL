package config_test

import (
	"testing"

	"i8sl/internal/config"
)

func TestLoadDefaultsUseSQLite(t *testing.T) {
	t.Setenv("I8SL_STORAGE_DRIVER", "")
	t.Setenv("I8SL_SQLITE_PATH", "")
	t.Setenv("I8SL_DB_URI", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.StorageDriver != "sqlite" {
		t.Fatalf("expected sqlite driver by default, got %q", cfg.StorageDriver)
	}

	if cfg.SQLitePath == "" {
		t.Fatal("expected default sqlite path to be set")
	}
}

func TestLoadAcceptsPostgresDriver(t *testing.T) {
	t.Setenv("I8SL_STORAGE_DRIVER", "postgres")
	t.Setenv("I8SL_DB_URI", "postgres://i8sl:i8sl@localhost:5432/i8sl?sslmode=disable")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.StorageDriver != "postgres" {
		t.Fatalf("expected postgres driver, got %q", cfg.StorageDriver)
	}
}

func TestLoadParsesTrustedProxiesAndLogLevel(t *testing.T) {
	t.Setenv("I8SL_TRUSTED_PROXIES", "127.0.0.1/32,::1/128")
	t.Setenv("I8SL_LOG_LEVEL", "debug")
	t.Setenv("I8SL_RATE_LIMIT_BACKEND", "redis")
	t.Setenv("I8SL_REDIS_DB", "2")
	t.Setenv("I8SL_TRACING_ENABLED", "true")
	t.Setenv("I8SL_OTLP_ENDPOINT", "localhost:4318")
	t.Setenv("I8SL_TRACE_SAMPLE_RATIO", "0.5")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got := len(cfg.TrustedProxies); got != 2 {
		t.Fatalf("expected 2 trusted proxies, got %d", got)
	}

	if cfg.LogLevel.String() != "DEBUG" {
		t.Fatalf("expected debug log level, got %s", cfg.LogLevel.String())
	}

	if cfg.RateLimitBackend != "redis" {
		t.Fatalf("expected redis rate limit backend, got %q", cfg.RateLimitBackend)
	}

	if cfg.RedisDB != 2 {
		t.Fatalf("expected redis db 2, got %d", cfg.RedisDB)
	}

	if !cfg.TracingEnabled {
		t.Fatal("expected tracing to be enabled")
	}

	if cfg.TraceSampleRatio != 0.5 {
		t.Fatalf("expected trace sample ratio 0.5, got %v", cfg.TraceSampleRatio)
	}
}
