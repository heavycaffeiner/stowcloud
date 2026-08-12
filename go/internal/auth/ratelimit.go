package auth

import "time"

// limiter is a per-key sliding-window limit for login attempts, keyed by the
// client address the trusted-proxy rule resolved. Bucketting on one function
// and one implementation is what keeps every visitor from collapsing onto a
// single key when behind the proxy.
type limiter struct {
	window time.Duration
	max    int

	// entries in insertion order for bounded eviction.
	now func() int64
	k   map[string]*limitBucket
	ord []string
}

type limitBucket struct {
	count int
	reset int64
}

func newLimiter(window time.Duration, max int, now func() int64) *limiter {
	return &limiter{window: window, max: max, now: now, k: map[string]*limitBucket{}}
}

// Allow reports whether the key may try again. A key that exhausts its budget
// is refused until its window resets.
func (l *limiter) Allow(key string) bool {
	if key == "" {
		key = "unknown"
	}
	now := l.now()
	b, ok := l.k[key]
	if !ok {
		if len(l.ord) >= 65536 {
			l.evictOne()
		}
		l.k[key] = &limitBucket{count: 1, reset: now + l.window.Nanoseconds()}
		l.ord = append(l.ord, key)
		return true
	}
	if now >= b.reset {
		b.count = 1
		b.reset = now + l.window.Nanoseconds()
		return true
	}
	if b.count >= l.max {
		return false
	}
	b.count++
	return true
}

func (l *limiter) evictOne() {
	if len(l.ord) == 0 {
		return
	}
	key := l.ord[0]
	l.ord = l.ord[1:]
	delete(l.k, key)
}
