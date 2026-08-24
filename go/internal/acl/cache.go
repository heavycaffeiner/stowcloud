package acl

import "sync"

// decisionCache is the bounded evaluator cache. Gen-aware: a stored decision
// is only returned while its generation matches, so a grant or membership
// change invalidates the whole cache without a sweep. FIFO eviction keeps the
// bound that stops an attacker's generated path set from growing memory.
type decisionCache struct {
	mu    sync.Mutex
	cap   int
	data  map[cacheKey]cacheEntry
	order []cacheKey
}

func newDecisionCache(capacity int) *decisionCache {
	return &decisionCache{cap: capacity, data: map[cacheKey]cacheEntry{}}
}

func (c *decisionCache) lookup(k cacheKey, gen int64) (bool, Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[k]
	if !ok || e.gen != gen {
		return false, Decision{}
	}
	return true, e.d
}

func (c *decisionCache) store(k cacheKey, gen int64, d Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[k]; !ok {
		if len(c.order) >= c.cap {
			old := c.order[0]
			c.order = c.order[1:]
			delete(c.data, old)
		}
		c.order = append(c.order, k)
	}
	c.data[k] = cacheEntry{gen: gen, d: d}
}
