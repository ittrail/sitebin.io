package auth

import (
	"sync"
	"time"
)

// Limiter is a keyed token bucket: perHour events sustained, bursts up to
// burst. Used for anonymous site creation (key = IP) and password-attempt
// throttling (key = IP|target).
type Limiter struct {
	Now func() time.Time

	rate  float64 // tokens per second
	burst float64
	mu    sync.Mutex
	m     map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter(perHour float64, burst int) *Limiter {
	return &Limiter{
		Now:   time.Now,
		rate:  perHour / 3600,
		burst: float64(burst),
		m:     make(map[string]*bucket),
	}
}

// Allow consumes one token for key if available.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	b, ok := l.m[key]
	if !ok {
		if len(l.m) > 65536 { // bound memory under address-spraying
			l.prune(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.m[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// prune drops buckets that have fully refilled (indistinguishable from new).
func (l *Limiter) prune(now time.Time) {
	for k, b := range l.m {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.m, k)
		}
	}
}
