# I8SL

I8SL is a Go URL shortener inspired by [STFU](https://github.com/pmswe/STFU). The project keeps the core idea simple - create a short link and redirect by code - but adds the operational parts that are usually missing in tiny demo services: health probes, expiration rules, rate limiting, structured logs, container packaging, PostgreSQL persistence for deployed environments, zero-config local startup, CI, and repository-hosted API docs.

## What the service does

I8SL exposes a small HTTP API that creates redirect rules and resolves them later through short codes.

Supported behavior:

- create short URLs from JSON or query parameters
- provide custom aliases when a generated code is not enough
- inspect stored rules and delete them explicitly
- expire rules by lifetime, usage count, or both at once
- reject abusive generation bursts per client IP
- publish OpenAPI documentation through Scalar locally and through GitHub Pages from the repository

## Features and why they exist

### Go HTTP API for generating and resolving short URLs

The service exposes `POST /api/v1/rules`, `GET /api/v1/generate`, and `GET /r/{code}` because a shortener only needs two core capabilities: creation and resolution. Both creation styles are kept on purpose:

- `POST /api/v1/rules` is the cleaner API-first contract for integrations
- `GET /api/v1/generate` matches the STFU-style usage and is convenient for quick manual calls

### Custom alias support

Aliases are supported because generated codes are not always enough. In admin tools, marketing links, demos, and internal documentation, readable codes are useful. Validation keeps aliases alphanumeric and bounded to avoid path ambiguity and weird edge cases.

### Rule inspection and deletion endpoints

`GET /api/v1/rules/{code}` and `DELETE /api/v1/rules/{code}` exist because a shortener without visibility becomes hard to operate. Inspection shows usage counters and expiration state. Deletion allows cleanup without touching the database directly.

### Scalar-powered OpenAPI reference at `/docs`

The running service serves Scalar at `/docs` because the API should document itself where it runs. Scalar is used instead of a custom HTML page because it gives a solid OpenAPI-first reference with minimal maintenance.

### Health checks at `/health/live` and `/health/ready`

Two probes are separated intentionally:

- `/health/live` answers whether the process is alive
- `/health/ready` verifies whether the backing store is reachable and the service is safe to receive traffic

This split is useful for containers, reverse proxies, and orchestrators.

### Rule expiration by `ttl_seconds`, `max_usages`, or both

Expiration rules are first-class because temporary and limited-use links are common real requirements. Keeping both options allows links such as:

- valid for one hour regardless of usage
- valid for five clicks regardless of time
- valid until either one hour passes or five clicks are consumed

### Generation rate limits per client IP

Short-link creation is the easiest place to abuse a public shortener. Per-IP rate limiting is kept simple and local because it is enough for this project scope and does not need Redis or another external dependency.

### Structured request logging with `log/slog`

`log/slog` is used because it is standard, lightweight, and already part of modern Go. The logs include method, path, status, duration, client IP, and source location. This is enough for local debugging and CI output without introducing an extra logging framework.

### PostgreSQL storage for service data

PostgreSQL is used as the primary deployed database because it matches the service-oriented shape of the project better than a file database. It gives explicit networking, production-friendly behavior, locking semantics for concurrent redirect updates, and a deployment model that fits Docker Compose and future hosted environments.

At the same time, `go run ./cmd/i8sl` now works without any external database because the default local storage driver is SQLite. That split is intentional:

- SQLite is the zero-setup developer default
- PostgreSQL is the deployment default in Docker/Compose
- the storage driver is selected explicitly through `I8SL_STORAGE_DRIVER`

### Docker Compose deployment file

`deployments/docker-compose.yml` is included because the project should be runnable with one command, persistent data, and a healthcheck. Compose is enough here; anything heavier would add more ops surface than value.

### GitHub Actions CI pipeline

CI exists so formatting, tests, build validity, and Docker image creation are checked on every push and pull request. This catches breakage early and makes the repository self-verifying.

### GitHub Pages API docs from the repository

GitHub Pages is added so the API reference is visible even when the service is not running locally. The Pages workflow builds a tiny static site from repository files and publishes Scalar with the committed OpenAPI spec.

## Tech Stack

- Language: Go 1.25
- HTTP: standard `net/http`
- Logging: `log/slog`
- Persistence: PostgreSQL via `pgx` for deployed runtime, SQLite for zero-config local dev and tests
- Rate limiting: `golang.org/x/time/rate`
- API spec: OpenAPI 3.1
- API UI: Scalar
- Tests: `testing` + `httptest`
- Delivery: Docker, Docker Compose, GitHub Actions, GitHub Pages

## Why the project is structured this way

The repository follows the spirit of `golang-standards/project-layout` because responsibilities are easier to read when entrypoints, runtime assets, and business code are separated.

```text
.
├── cmd/i8sl                  # thin executable entrypoint
├── configs                   # example environment configuration
├── deployments               # docker-compose deployment files
├── docs                      # embedded docs assets and Pages source
├── build/package             # Docker packaging
├── internal/app/i8sl         # application bootstrap and wiring
├── internal/code             # short-code generator
├── internal/config           # environment config loading
├── internal/ratelimit        # generation rate limiting
├── internal/server           # HTTP handlers, middleware, response shaping
├── internal/shortener        # business rules and validations
├── internal/storage/postgres # production persistence implementation
├── internal/storage/sqlite   # lightweight test persistence
└── .github/workflows         # CI and Pages deployment
```

Why not keep everything in `main.go` or one package:

- bootstrap code should not be mixed with domain rules
- HTTP formatting should not be mixed with database access
- docs assets should live outside `internal` because they are also repository assets now
- deploy and CI files should be visible at the repository level, not hidden inside application code

Note: there is no active `internal/docs` directory anymore. Docs now live in root `docs`, because they are both runtime assets and repository-level documentation artifacts.

## Endpoints

- `POST /api/v1/rules` - create a rule from JSON
- `GET /api/v1/generate` - create a rule from query params
- `GET /api/v1/rules/{code}` - inspect a rule
- `DELETE /api/v1/rules/{code}` - delete a rule
- `GET /r/{code}` - resolve and redirect
- `GET /health/live` - liveness probe
- `GET /health/ready` - readiness probe
- `GET /docs` - local Scalar API docs
- GitHub Pages - repository-hosted static Scalar docs

## Runtime flow

1. `cmd/i8sl` starts the app.
2. `internal/app/i8sl` loads config, logger, storage, service, and HTTP server.
3. `internal/server` validates requests and maps HTTP to domain calls.
4. `internal/shortener` applies creation rules, expiration logic, and code validation.
5. `internal/storage/postgres` persists and resolves rules in deployed runtime.
6. `internal/storage/sqlite` powers local quick-start and tests when the storage driver is `sqlite`.
7. `docs` serves embedded HTML and OpenAPI files for local documentation.

## Quick Start

```bash
go run ./cmd/i8sl
```

The service starts on `:8080` by default and now uses SQLite automatically for local startup, so a fresh `go run` works without PostgreSQL.

Open:

- `http://localhost:8080/`
- `http://localhost:8080/docs`

## Configuration

Environment variables:

- `I8SL_HTTP_ADDR` - bind address, default `:8080`
- `I8SL_BASE_URL` - public base URL used in API responses; set this behind reverse proxies or public domains
- `I8SL_STORAGE_DRIVER` - `sqlite` for local zero-config mode or `postgres` for deployed mode; default `sqlite`
- `I8SL_SQLITE_PATH` - local SQLite path used when driver is `sqlite`; default `./i8sl.db`
- `I8SL_DB_URI` - PostgreSQL connection string used when driver is `postgres`
- `I8SL_CODE_LENGTH` - generated code length, default `7`
- `I8SL_GENERATION_RATE_PER_MINUTE` - generation quota per IP, default `30`
- `I8SL_GENERATION_BURST` - allowed burst size per IP, default `10`

Use `configs/i8sl.env.example` as a starting point.

Typical modes:

- local quick start: keep the default `sqlite`
- local PostgreSQL run: set `I8SL_STORAGE_DRIVER=postgres`
- Docker Compose: already configured for PostgreSQL

## Example Requests

Create a rule:

```bash
curl -X POST http://localhost:8080/api/v1/rules \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://example.com/golang",
    "ttl_seconds": 3600,
    "max_usages": 5
  }'
```

Create a rule with a custom alias:

```bash
curl -X POST http://localhost:8080/api/v1/rules \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://example.com/release-notes",
    "alias": "release01"
  }'
```

Generate STFU-style from query params:

```bash
curl "http://localhost:8080/api/v1/generate?url=https://example.com/docs&max_usages=3"
```

Inspect a rule:

```bash
curl http://localhost:8080/api/v1/rules/abc123
```

Delete a rule:

```bash
curl -X DELETE http://localhost:8080/api/v1/rules/abc123
```

## Running Tests

Use verbose mode to see structured request logs from the test server:

```bash
go test -v ./...
```

## Docker

```bash
docker build -f build/package/Dockerfile -t i8sl .
docker run --rm -p 8080:8080 \
  -e I8SL_DB_URI="postgres://i8sl:i8sl@host.docker.internal:5432/i8sl?sslmode=disable" \
  i8sl
```

## Docker Compose

```bash
docker compose -f deployments/docker-compose.yml up --build
```

## GitHub Actions

CI workflow: `.github/workflows/ci.yml`

It verifies:

- module dependency resolution
- formatting through `gofmt`
- verbose test execution
- application build
- Docker image build

## GitHub Pages

Pages workflow: `.github/workflows/pages.yml`

It publishes a static Scalar site built from:

- `docs/pages/index.html`
- `docs/static/openapi.yaml`

Typical Pages URL:

```text
https://<owner>.github.io/<repository>/
```

To enable it in GitHub:

1. Open repository Settings.
2. Go to Pages.
3. Ensure the source is GitHub Actions.
4. Push to `main` or run the Pages workflow manually.
