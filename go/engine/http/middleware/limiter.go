// Linux only, for the same reason as the rest of this package.
//go:build linux

// The request limiter: a token bucket per resolved client, over an injected
// clock so the tests measure refill rather than sleep for it.
package middleware

import (
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// LimiterCap bounds how many client buckets are held at once.
//
// A bounded map is the whole point: an unbounded one is a memory leak driven
// by whoever sends the most distinct source addresses, which is the same party
// the limiter exists to restrain.
const LimiterCap = 65536

// Limiter is a token bucket per client key.
//
// Safe for concurrent use. The rate and burst are replaceable at runtime,
// because they are operator settings and a change should not need a restart.
type Limiter struct {
	clk clock.Clock

	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
}

type bucket struct {
	tokens float64
	lastNs int64
}

// NewLimiter builds a limiter. A non-positive rate or burst is refused by
// clamping to a floor of one, because a zero would refuse every request and a
// negative one has no meaning.
func NewLimiter(clk clock.Clock, ratePerSecond, burst float64) *Limiter {
	l := &Limiter{clk: clk, buckets: make(map[string]*bucket)}
	l.SetLimits(ratePerSecond, burst)
	return l
}

// SetLimits replaces the rate and burst atomically.
//
// Existing buckets keep their current token counts. Resetting them would let
// anyone who can cause a settings write also clear their own throttle.
func (l *Limiter) SetLimits(ratePerSecond, burst float64) {
	if ratePerSecond <= 0 {
		ratePerSecond = 1
	}
	if burst < 1 {
		burst = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate, l.burst = ratePerSecond, burst
}

// Allow spends one token for key, reporting whether there was one.
func (l *Limiter) Allow(key string) bool {
	now := l.clk.Nanos()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= LimiterCap {
			// Evicting an arbitrary bucket is deliberate. The alternative is
			// refusing the new client, which turns a full map into a denial of
			// service against everyone who arrives after it fills.
			l.evictOne()
		}
		b = &bucket{tokens: l.burst, lastNs: now}
		l.buckets[key] = b
	}

	elapsed := float64(now-b.lastNs) / float64(time.Second)
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastNs = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictOne drops one bucket. The caller holds the lock.
func (l *Limiter) evictOne() {
	for k := range l.buckets {
		delete(l.buckets, k)
		return
	}
}

// Size reports how many buckets are held, for the cap's own test.
func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
