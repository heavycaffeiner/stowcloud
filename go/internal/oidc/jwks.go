package oidc

import (
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// jwksCache holds the provider's key set for a bounded time. A set cached
// without a bound is a key rotation this server never notices.
type jwksCache struct {
	mu      sync.Mutex
	keys    []jwk
	fetched time.Time
}

func (c *jwksCache) fresh(clk clock.Clock) []jwk {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil || clk.Now().Sub(c.fetched) >= limits.OIDCJWKSTTL {
		return nil
	}
	return c.keys
}

func (c *jwksCache) store(keys []jwk, clk clock.Clock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys, c.fetched = keys, clk.Now()
}

func (c *jwksCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = nil
}
