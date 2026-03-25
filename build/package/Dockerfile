FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/i8sl ./cmd/i8sl

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=build /out/i8sl /usr/local/bin/i8sl

ENV I8SL_HTTP_ADDR=:8080
ENV I8SL_STORAGE_DRIVER=postgres
ENV I8SL_DB_URI=postgres://i8sl:i8sl@postgres:5432/i8sl?sslmode=disable

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/i8sl"]
