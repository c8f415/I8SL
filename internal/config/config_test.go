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
