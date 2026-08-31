package oidc

import (
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// ttlCache is one slot with a bounded lifetime, used for the discovery
// document and for the key set.
//
// It is one implementation used twice rather than two, because the two rules
// that matter are the same in both places and each was a chance to get one of
// them wrong.
//
// Only a success is stored. A provider that was down a second ago may be up
// now, and caching the failure would turn a one-second outage into an hour of
// them.
//
// Two callers on a cold cache both fetch, which is accepted rather than
// solved: deduplicating them means holding a lock across a network call, and
// the cost of the race is one extra request.
type ttlCache[T any] struct {
	ttl time.Duration

	mu      sync.Mutex
	value   T
	stored  bool
	fetched time.Time
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] {
	return &ttlCache[T]{ttl: ttl}
}

// fresh returns the stored value while it is inside its lifetime.
func (c *ttlCache[T]) fresh(clk clock.Clock) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	if !c.stored || clk.Now().Sub(c.fetched) >= c.ttl {
		return zero, false
	}
	return c.value, true
}

func (c *ttlCache[T]) store(v T, clk clock.Clock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value, c.stored, c.fetched = v, true, clk.Now()
}

// forget drops the value, which is what a key the set does not carry means:
// the set is refetched once rather than treated as final.
func (c *ttlCache[T]) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	c.value, c.stored = zero, false
}
