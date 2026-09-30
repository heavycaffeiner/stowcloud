package acl

import "sync"

// decisionCacheCapacity bounds the cache against a client that can generate
// many distinct paths. It holds regardless of how often grants change, which
// is a separate concern from invalidation.
const decisionCacheCapacity = 4096

type cacheKey struct {
	user  int64
	share int64
	path  string
	want  Perms
}

type cacheEntry struct {
	gen int64
	d   Decision
}

// decisionCache memoizes Decision values. Invalidation is generational, not
// swept: an entry carries the gen it was computed under, and a lookup against
// a different gen misses. A reload only bumps gen, which makes every earlier
// entry unreachable without walking the map.
//
// Its mutex is separate from the evaluator's, so a lookup never blocks a
// reload. The two are correct together because a lookup only trusts an entry
// whose gen matches a value the caller already read under the evaluator's
// lock, never one the cache invents.
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

// store inserts or refreshes an entry, evicting the oldest key on insert past
// capacity.
func (c *decisionCache) store(k cacheKey, gen int64, d Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[k]; !ok {
		if len(c.order) >= c.cap {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.data, oldest)
		}
		c.order = append(c.order, k)
	}
	c.data[k] = cacheEntry{gen: gen, d: d}
}
