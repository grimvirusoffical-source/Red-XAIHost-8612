package host

import (
	"sync"
	"time"
)

// bucket is a token bucket.
type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a keyed token-bucket rate limiter.
type Limiter struct {
	mu  sync.Mutex
	m   map[string]*bucket
	now func() time.Time
}

// NewLimiter creates a limiter; now may be nil.
func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{m: map[string]*bucket{}, now: now}
}

// Allow takes one token from the bucket for key (rps tokens/sec, burst capacity).
// rps<0 means unlimited. When denied, retry is the suggested wait.
func (l *Limiter) Allow(key string, rps float64, burst int) (ok bool, retry time.Duration) {
	if rps < 0 {
		return true, 0
	}
	if burst < 1 {
		burst = 1
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[key]
	if b == nil {
		if len(l.m) > 20000 {
			l.m = map[string]*bucket{}
		}
		b = &bucket{tokens: float64(burst), last: now}
		l.m[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * rps
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	if rps == 0 {
		return false, time.Minute
	}
	return false, time.Duration((1 - b.tokens) / rps * float64(time.Second))
}
