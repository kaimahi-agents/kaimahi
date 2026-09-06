package inbound

import (
	"sync"
	"time"
)

// limiter is a per-hook token bucket. In-memory and PER REPLICA on
// purpose: it is a flood guard, not a governance decision, and it
// runs before authentication — a bucket shared through the store would
// be a store write per event, which is exactly the amplification the
// limiter exists to bound. The effective ceiling is therefore replicas ×
// the configured rate (docs/operations.md); every limit that IS a
// governance decision is exact in Postgres.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{buckets: map[string]*bucket{}, now: now}
}

// allow takes one token from the hook's bucket (capacity burst, refilled
// at ratePerMinute), reporting whether one was available.
func (l *limiter) allow(hook string, ratePerMinute, burst int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[hook]
	if !ok {
		b = &bucket{tokens: float64(burst), last: now}
		l.buckets[hook] = b
	}
	elapsed := now.Sub(b.last).Minutes()
	if elapsed > 0 {
		b.tokens = min(float64(burst), b.tokens+elapsed*float64(ratePerMinute))
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
