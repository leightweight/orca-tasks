package server

import (
	"context"
	"time"

	"github.com/sethvargo/go-limiter"
	"github.com/sethvargo/go-limiter/memorystore"
)

// Failure limits: this many failed attempts per key in the window, then 429
// until the window passes.
const (
	maxFailures   = 10
	failureWindow = 15 * time.Minute
)

// Limiter counts failed sign-ins and API key checks per client IP and per
// username. Successes don't count. The counts are in memory, which suits the
// one replica the chart runs.
type Limiter struct {
	store limiter.Store
}

// NewLimiter returns a limiter with the standard limits.
func NewLimiter() (*Limiter, error) {
	store, err := memorystore.New(&memorystore.Config{Tokens: maxFailures, Interval: failureWindow})
	if err != nil {
		return nil, err
	}

	return &Limiter{store: store}, nil
}

// Allowed reports whether every key still has attempts left. A key with no
// failures has no bucket yet, which counts as allowed.
func (l *Limiter) Allowed(ctx context.Context, keys ...string) bool {
	for _, key := range keys {
		tokens, remaining, err := l.store.Get(ctx, key)
		if err != nil || (tokens > 0 && remaining == 0) {
			return false
		}
	}

	return true
}

// Failed counts a failure against each key.
func (l *Limiter) Failed(ctx context.Context, keys ...string) {
	for _, key := range keys {
		_, _, _, _, _ = l.store.Take(ctx, key)
	}
}

// Close stops the limiter's cleanup.
func (l *Limiter) Close(ctx context.Context) error {
	return l.store.Close(ctx)
}
