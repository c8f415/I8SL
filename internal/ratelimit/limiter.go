package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type Limiter struct {
	limit rate.Limit
	burst int
	ttl   time.Duration

	mu      sync.Mutex
	clients map[string]*client
}

func New(perMinute float64, burst int, ttl time.Duration) *Limiter {
	return &Limiter{
		limit:   rate.Limit(perMinute / 60),
		burst:   burst,
		ttl:     ttl,
		clients: make(map[string]*client),
	}
}

func (l *Limiter) Allow(key string) bool {
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

	return entry.limiter.Allow()
}

func (l *Limiter) cleanup(now time.Time) {
	for key, entry := range l.clients {
		if now.Sub(entry.lastSeen) > l.ttl {
			delete(l.clients, key)
		}
	}
}
