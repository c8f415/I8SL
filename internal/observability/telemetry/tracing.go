package telemetry

import (
	"context"
	"fmt"

	"i8sl/internal/config"
	"i8sl/internal/observability/buildinfo"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func Setup(ctx context.Context, cfg config.Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !cfg.TracingEnabled {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	exporterOptions := make([]otlptracehttp.Option, 0, 2)
	if cfg.OTLPEndpoint != "" {
		exporterOptions = append(exporterOptions, otlptracehttp.WithEndpoint(cfg.OTLPEndpoint))
	}
	if cfg.OTLPInsecure {
		exporterOptions = append(exporterOptions, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, exporterOptions...)
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("deployment.environment", cfg.Environment),
			attribute.String("service.version", buildinfo.Version),
			attribute.String("service.commit", buildinfo.Commit),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create otel resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio)),
	)

	otel.SetTracerProvider(provider)

	return provider.Shutdown, nil
}
