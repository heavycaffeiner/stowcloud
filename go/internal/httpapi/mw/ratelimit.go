package mw

import (
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// RateLimiter is the shared token bucket for step 5. One instance in the
// state, keyed by the client address TrustedProxy resolved, so every visitor
// has their own bucket and the unattributable placeholder has the one bucket
// it shares with nothing.
//
// The bucket discipline is the standard one: capacity is the burst, a refill
// of refillPerSecond tokens accrues continuously, and a refusal carries the
// seconds until a token is available. The clock is injected so the window is
// testable and the bound is a number the settings surface may move within its
// D5 outer bound, never a constant this file owns.
type RateLimiter struct {
	capacity float64
	refill   float64
	now      func() int64

	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
}

type bucket struct {
	tokens float64
	last   int64
}

// NewRateLimiter builds a limiter. rate is tokens per second and burst is the
// initial allowance; both must be positive.
func NewRateLimiter(rate float64, burst int, clk clock.Clock) *RateLimiter {
	return &RateLimiter{
		capacity: float64(burst),
		refill:   rate,
		now:      clk.Nanos,
		buckets:  map[netip.Addr]*bucket{},
	}
}

// Allow spends one token for addr. It returns the seconds to wait when the
// bucket is dry, and zero when the request may proceed.
func (l *RateLimiter) Allow(addr netip.Addr) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[addr]
	if !ok {
		if len(l.buckets) >= 65536 {
			// A bounded map: an eviction of the oldest entry is the price of
			// not letting a flood of fresh addresses grow memory without
			// bound. Any entry is fair game; the bucket discipline resets it.
			for k := range l.buckets {
				delete(l.buckets, k)
				break
			}
		}
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[addr] = b
	}
	elapsed := float64(now-b.last) / float64(time.Second.Nanoseconds())
	b.tokens = minF(b.tokens+elapsed*l.refill, l.capacity)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	wait := (1 - b.tokens) / l.refill
	if wait < 1 {
		wait = 1
	}
	return time.Duration(wait * float64(time.Second.Nanoseconds()))
}

// Set moves the rate and burst within the compiled-in bound. The settings
// surface calls this after persisting; nothing on a request path may.
func (l *RateLimiter) Set(rate float64, burst int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill = rate
	l.capacity = float64(burst)
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// RateLimit is step 5. It sits before Auth so a flood of unauthenticated
// requests is refused before it costs an Argon2 invocation, and before
// BodyLimit so the refusal also happens before a body is read.
func RateLimit(l *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wait := l.Allow(ClientFrom(r.Context()))
			if wait == 0 {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		})
	}
}
