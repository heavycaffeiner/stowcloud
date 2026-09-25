//go:build linux

package links

import (
	"sync"
	"time"
)

type Limiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	now     func() int64
	buckets map[string]*bucket
	order   []string
}
type bucket struct {
	count int
	reset int64
}

const limiterKeys = 65536

func NewLimiter(window time.Duration, max int, now func() int64) *Limiter {
	return &Limiter{window: window, max: max, now: now, buckets: make(map[string]*bucket)}
}
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok || now >= b.reset {
		if !ok {
			if len(l.order) >= limiterKeys {
				delete(l.buckets, l.order[0])
				l.order = l.order[1:]
			}
			l.order = append(l.order, key)
		}
		l.buckets[key] = &bucket{count: 1, reset: now + l.window.Nanoseconds()}
		return true
	}
	if b.count >= l.max {
		return false
	}
	b.count++
	return true
}
