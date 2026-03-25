# I8SL Operations

## Local stack

Run the full local stack:

```bash
docker compose -f deployments/docker-compose.yml up --build
```

Services:

- `i8sl` - main API service
- `postgres` - primary database
- `redis` - distributed rate limiting backend
- `otel-collector` - receives traces from I8SL
- `prometheus` - scrapes `/metrics`

## Useful URLs

- App: `http://localhost:8080/`
- Scalar docs: `http://localhost:8080/docs`
- Health live: `http://localhost:8080/health/live`
- Health ready: `http://localhost:8080/health/ready`
- Metrics: `http://localhost:8080/metrics`
- Prometheus UI: `http://localhost:9090/`

## Default stack mode in Compose

Compose runs I8SL with:

- PostgreSQL storage
- Redis rate limiting
- OpenTelemetry tracing enabled
- Prometheus metrics scraping

## Quick checks

Readiness:

```bash
curl http://localhost:8080/health/ready
```

Metrics:

```bash
curl http://localhost:8080/metrics
```

Create a rule:

```bash
curl -X POST http://localhost:8080/api/v1/rules \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com"}'
```

## Notes

- For simple local development without containers, `go run ./cmd/i8sl` still defaults to SQLite and in-memory rate limiting.
- For tracing, the collector currently prints spans to its logs through the `debug` exporter.
