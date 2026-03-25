package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var tokenBucketScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

local values = redis.call("HMGET", KEYS[1], "tokens", "last")
local tokens = tonumber(values[1])
local last = tonumber(values[2])

if tokens == nil then
  tokens = burst
  last = now
else
  if last == nil then
    last = now
  end
  local elapsed = math.max(0, now - last) / 1000.0
  tokens = math.min(burst, tokens + (elapsed * rate))
end

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call("HSET", KEYS[1], "tokens", tokens, "last", now)
redis.call("PEXPIRE", KEYS[1], ttl)

return allowed
`)

type RedisLimiter struct {
	client     *redis.Client
	ratePerSec float64
	burst      int
	stateTTL   time.Duration
	keyPrefix  string
}

func NewRedis(addr, password string, db int, perMinute float64, burst int, ttl time.Duration, keyPrefix string) (*RedisLimiter, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("redis addr is required")
	}

	if strings.TrimSpace(keyPrefix) == "" {
		keyPrefix = "i8sl:rate_limit"
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	stateTTL := ttl
	if perMinute > 0 && burst > 0 {
		refillDuration := time.Duration((float64(burst) / perMinute) * 60 * float64(time.Second))
		if refillDuration > stateTTL {
			stateTTL = refillDuration
		}
	}
	if stateTTL < time.Minute {
		stateTTL = time.Minute
	}

	return &RedisLimiter{
		client:     client,
		ratePerSec: perMinute / 60,
		burst:      burst,
		stateTTL:   stateTTL,
		keyPrefix:  keyPrefix,
	}, nil
}

func (l *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {
	if key == "" {
		key = "unknown"
	}

	result, err := tokenBucketScript.Run(
		ctx,
		l.client,
		[]string{l.keyPrefix + ":" + key},
		time.Now().UTC().UnixMilli(),
		l.ratePerSec,
		l.burst,
		l.stateTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("redis rate limit: %w", err)
	}

	return result == 1, nil
}

func (l *RedisLimiter) Ping(ctx context.Context) error {
	return l.client.Ping(ctx).Err()
}

func (l *RedisLimiter) Close() error {
	return l.client.Close()
}
