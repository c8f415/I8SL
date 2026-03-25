package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ServiceName             string
	HTTPAddr                string
	BaseURL                 string
	StorageDriver           string
	SQLitePath              string
	DBURI                   string
	CodeLength              int
	GenerationRatePerMinute float64
	GenerationBurst         int
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

	storageDriver := strings.ToLower(envString("I8SL_STORAGE_DRIVER", "sqlite"))
	if storageDriver != "sqlite" && storageDriver != "postgres" {
		return Config{}, fmt.Errorf("I8SL_STORAGE_DRIVER must be either sqlite or postgres")
	}

	return Config{
		ServiceName:             "I8SL",
		HTTPAddr:                envString("I8SL_HTTP_ADDR", ":8080"),
		BaseURL:                 baseURL,
		StorageDriver:           storageDriver,
		SQLitePath:              envString("I8SL_SQLITE_PATH", "./i8sl.db"),
		DBURI:                   envString("I8SL_DB_URI", "postgres://i8sl:i8sl@localhost:5432/i8sl?sslmode=disable"),
		CodeLength:              codeLength,
		GenerationRatePerMinute: ratePerMinute,
		GenerationBurst:         burst,
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
