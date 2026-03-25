package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type MemoryLimiter struct {
	limit rate.Limit
	burst int
	ttl   time.Duration

	mu      sync.Mutex
	clients map[string]*client
}

func NewMemory(perMinute float64, burst int, ttl time.Duration) *MemoryLimiter {
	return &MemoryLimiter{
		limit:   rate.Limit(perMinute / 60),
		burst:   burst,
		ttl:     ttl,
		clients: make(map[string]*client),
	}
}

func (l *MemoryLimiter) Allow(_ context.Context, key string) (bool, error) {
	if key == "" {
		key = "unknown"
	}

	now := time.Now().UTC()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.cleanup(now)

	entry, ok := l.clients[key]
	if !ok {
		entry = &client{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = entry
	}

	entry.lastSeen = now

	return entry.limiter.Allow(), nil
}

func (l *MemoryLimiter) Ping(context.Context) error {
	return nil
}

func (l *MemoryLimiter) Close() error {
	return nil
}

func (l *MemoryLimiter) cleanup(now time.Time) {
	for key, entry := range l.clients {
		if now.Sub(entry.lastSeen) > l.ttl {
			delete(l.clients, key)
		}
	}
}
