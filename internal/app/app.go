package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"i8sl/internal/code"
	"i8sl/internal/config"
	"i8sl/internal/observability/buildinfo"
	"i8sl/internal/observability/telemetry"
	"i8sl/internal/ratelimit"
	"i8sl/internal/server"
	"i8sl/internal/shortener"
	"i8sl/internal/storage/postgres"
	"i8sl/internal/storage/sqlite"
)

func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     cfg.LogLevel,
	}))

	telemetryShutdown, err := telemetry.Setup(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("setup telemetry: %w", err)
	}
	defer func() {
		if err := telemetryShutdown(context.Background()); err != nil {
			logger.Error("shutdown telemetry", "error", err)
		}
	}()

	store, storageLabel, err := openStore(cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	rateLimiter, limiterLabel, err := openRateLimiter(cfg)
	if err != nil {
		return fmt.Errorf("open rate limiter: %w", err)
	}
	defer rateLimiter.Close()

	svc := shortener.NewService(
		store,
		code.NewGenerator(cfg.CodeLength),
		cfg.BaseURL,
		shortener.WithPrivateTargetRejection(cfg.RejectPrivateTargets),
	)
	handler := server.NewHandler(cfg, logger, svc, rateLimiter)

	httpServer := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	defer cleanupCancel()

	if cfg.CleanupInterval > 0 {
		go runCleanup(cleanupCtx, logger, svc, cfg.CleanupInterval)
	}

	go func() {
		logger.Info(
			"starting service",
			"service", cfg.ServiceName,
			"env", cfg.Environment,
			"addr", cfg.HTTPAddr,
			"storage", storageLabel,
			"rate_limit_backend", limiterLabel,
			"tracing", cfg.TracingEnabled,
			"version", buildinfo.Version,
			"commit", buildinfo.Commit,
		)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server stopped unexpectedly: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown failed: %w", err)
	}

	logger.Info("service stopped")

	return nil
}

func runCleanup(ctx context.Context, logger *slog.Logger, svc *shortener.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := svc.DeleteExpired(ctx)
			if err != nil {
				logger.Error("cleanup expired rules", "error", err)
				continue
			}

			if deleted > 0 {
				logger.Info("cleanup expired rules", "deleted", deleted)
			}
		}
	}
}

func openStore(cfg config.Config) (shortener.Store, string, error) {
	switch cfg.StorageDriver {
	case "sqlite":
		store, err := sqlite.NewStore(cfg.SQLitePath)
		if err != nil {
			return nil, "", err
		}

		return store, "sqlite", nil
	case "postgres":
		store, err := postgres.NewStore(cfg.DBURI)
		if err != nil {
			return nil, "", err
		}

		return store, postgresLabel(cfg.DBURI), nil
	default:
		return nil, "", fmt.Errorf("unsupported storage driver %q", cfg.StorageDriver)
	}
}

func openRateLimiter(cfg config.Config) (ratelimit.Limiter, string, error) {
	switch cfg.RateLimitBackend {
	case "memory":
		return ratelimit.NewMemory(cfg.GenerationRatePerMinute, cfg.GenerationBurst, 10*time.Minute), "memory", nil
	case "redis":
		limiter, err := ratelimit.NewRedis(
			cfg.RedisAddr,
			cfg.RedisPassword,
			cfg.RedisDB,
			cfg.GenerationRatePerMinute,
			cfg.GenerationBurst,
			10*time.Minute,
			cfg.RedisKeyPrefix,
		)
		if err != nil {
			return nil, "", err
		}

		if err := limiter.Ping(context.Background()); err != nil {
			_ = limiter.Close()
			return nil, "", err
		}

		return limiter, "redis@" + cfg.RedisAddr, nil
	default:
		return nil, "", fmt.Errorf("unsupported rate limit backend %q", cfg.RateLimitBackend)
	}
}

func postgresLabel(rawURI string) string {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return "postgres"
	}

	host := parsed.Hostname()
	if host == "" {
		return "postgres"
	}

	database := parsed.Path
	if len(database) > 1 {
		database = database[1:]
	}

	if database == "" {
		return "postgres@" + host
	}

	return "postgres@" + host + "/" + database
}
