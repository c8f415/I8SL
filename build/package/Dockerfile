FROM golang:1.25-alpine AS build

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X i8sl/internal/observability/buildinfo.Version=${VERSION} -X i8sl/internal/observability/buildinfo.Commit=${COMMIT} -X i8sl/internal/observability/buildinfo.BuildTime=${BUILD_TIME}" -o /out/i8sl ./cmd/i8sl

FROM alpine:3.22

RUN apk add --no-cache ca-certificates && addgroup -S i8sl && adduser -S -G i8sl -h /app i8sl

WORKDIR /app

COPY --from=build /out/i8sl /usr/local/bin/i8sl

ENV I8SL_ENV=production
ENV I8SL_HTTP_ADDR=:8080
ENV I8SL_STORAGE_DRIVER=postgres
ENV I8SL_DB_URI=postgres://i8sl:i8sl@postgres:5432/i8sl?sslmode=disable

EXPOSE 8080

USER i8sl

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget -q -O - http://127.0.0.1:8080/health/ready >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/i8sl"]
