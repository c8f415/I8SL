package ratelimit

import "context"

type Limiter interface {
	Allow(context.Context, string) (bool, error)
	Ping(context.Context) error
	Close() error
}
