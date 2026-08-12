package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// Principal is what a successful credential resolves to, cached so a
// keep-alive-less client does not re-run Argon2 or a database lookup on every
// request.
type Principal struct {
	UserID   int64
	Display  string
	Disabled bool
}

// The three tiers of the verification path and their lifetimes, all of them
// invalidated together by the auth_generation counter. A password change, an
// app-password revocation or an account disable bumps that counter, so every
// entry in every tier goes stale by comparison rather than by being found and
// deleted: revocation is immediate on a surface that never re-reads the
// database.
const (
	// The tier-1 connection memo and the tier-3 app-password cache are LRUs
	// keyed by a digest with no TTL of their own beyond the generation counter.
	connMemoCapacity = 4096
	tokenCacheCap    = 1024

	// Tier 2: positive results live 15 minutes absolute, 5 minutes idle, so
	// an active sync stays warm and an abandoned client's entries evaporate.
	// Negative results live 30 seconds, because a client looping with a wrong
	// password would otherwise be a full Argon2 invocation on their behalf
	// and the server's, which is the denial of service arriving from the
	// direction nobody watches.
	credPositiveTTL  = 15 * time.Minute
	credPositiveIdle = 5 * time.Minute
	credNegativeTTL  = 30 * time.Second

	// App passwords are high-entropy and do not need a memory-hard function
	// to be safe, so a verified token bypasses tier 3 for a minute.
	tokenTTL = 60 * time.Second
)

// Outcome is a cached tier-2 or tier-3 decision.
type Outcome struct {
	Accepted  bool
	Principal Principal
}

type credEntry struct {
	outcome  Outcome
	gen      int64
	inserted time.Time
	lastHit  time.Time
}

type connEntry struct {
	principal Principal
	gen       int64
}

type tokenEntry struct {
	principal Principal
	gen       int64
	inserted  time.Time
}

// caches is the three tiers plus the ephemeral key that names tier-2 entries.
// All of them live and die with the process: the key comes from crypto/rand
// and is never on disk, so a process dump yields nothing offline-attackable.
type caches struct {
	ephemeral [32]byte

	credential *lru[[16]byte, credEntry]
	connMemo   *lru[[32]byte, connEntry]
	token      *lru[[32]byte, tokenEntry]

	clk clock.Clock
}

func newCaches(clk clock.Clock) *caches {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		// crypto/rand failing means the system RNG is gone; a cache that can
		// name its entries with a predictable key is worse than no cache.
		panic("crypto/rand failed to seed the credential cache: " + err.Error())
	}
	return &caches{
		ephemeral:  key,
		credential: newLRU[[16]byte, credEntry](4096),
		connMemo:   newLRU[[32]byte, connEntry](connMemoCapacity),
		token:      newLRU[[32]byte, tokenEntry](tokenCacheCap),
		clk:        clk,
	}
}

// tier2Key derives the cache key for one (user, password) pair: an HMAC under
// the per-process ephemeral key, never a hash of the password.
func (c *caches) Tier2Hash(user, pw string) [16]byte {
	input := make([]byte, 0, len(user)+len(pw)+2)
	input = append(input, 'd', 'a', 'v', 0)
	input = append(input, user...)
	input = append(input, 0)
	input = append(input, pw...)
	mac := hmac.New(sha256.New, c.ephemeral[:])
	mac.Write(input) //nolint:errcheck // hash.Hash.Write never fails.
	var out [16]byte
	copy(out[:], mac.Sum(nil)[:16])
	return out
}

// Tier2Lookup returns the cached outcome, or ok=false on a miss or an entry a
// generation or a TTL has made stale.
func (c *caches) Tier2Lookup(key [16]byte, gen int64) (Outcome, bool) {
	c.credential.mu.Lock()
	defer c.credential.mu.Unlock()
	e, present := c.credential.peek(key)
	if !present {
		return Outcome{}, false
	}
	now := c.clk.Now()
	switch {
	case e.gen != gen:
		c.credential.remove(key)
		return Outcome{}, false
	case e.outcome.Accepted:
		stale := now.Sub(e.inserted) > credPositiveTTL || now.Sub(e.lastHit) > credPositiveIdle
		if stale {
			c.credential.remove(key)
			return Outcome{}, false
		}
		e.lastHit = now
	default:
		if now.Sub(e.inserted) > credNegativeTTL {
			c.credential.remove(key)
			return Outcome{}, false
		}
	}
	e, _ = c.credential.peek(key)
	return e.outcome, true
}

func (c *caches) Tier2Store(key [16]byte, o Outcome, gen int64) {
	c.credential.mu.Lock()
	defer c.credential.mu.Unlock()
	now := c.clk.Now()
	c.credential.put(key, credEntry{outcome: o, gen: gen, inserted: now, lastHit: now})
}

// tier1Lookup is the connection memo: the resolved principal for a presented
// Authorization header, keyed by its SHA-256, invalidated by nothing but the
// generation counter.
func (c *caches) Tier1Lookup(hash [32]byte, gen int64) (Principal, bool) {
	c.connMemo.mu.Lock()
	defer c.connMemo.mu.Unlock()
	e, present := c.connMemo.peek(hash)
	if !present {
		return Principal{}, false
	}
	if e.gen != gen {
		c.connMemo.remove(hash)
		return Principal{}, false
	}
	return e.principal, true
}

func (c *caches) Tier1Store(hash [32]byte, p Principal, gen int64) {
	c.connMemo.mu.Lock()
	defer c.connMemo.mu.Unlock()
	c.connMemo.put(hash, connEntry{principal: p, gen: gen})
}

// tokenLookup is the app-password tier-3 bypass, keyed by sha256(token) with
// a 60-second TTL and the same generation invalidation as the other two
// tiers.
func (c *caches) TokenLookup(hash [32]byte, gen int64) (Principal, bool) {
	c.token.mu.Lock()
	defer c.token.mu.Unlock()
	e, present := c.token.peek(hash)
	if !present {
		return Principal{}, false
	}
	switch {
	case e.gen != gen:
		c.token.remove(hash)
		return Principal{}, false
	case c.clk.Now().Sub(e.inserted) > tokenTTL:
		c.token.remove(hash)
		return Principal{}, false
	}
	return e.principal, true
}

func (c *caches) TokenStore(hash [32]byte, p Principal, gen int64) {
	c.token.mu.Lock()
	defer c.token.mu.Unlock()
	c.token.put(hash, tokenEntry{principal: p, gen: gen, inserted: c.clk.Now()})
}

// lru is a bounded map with FIFO-evicted entries, guarded by its own mutex.
// Access-order eviction would take a list of links to do properly; FIFO keeps
// the bound that stops an attacker filling memory and is all a cache of
// verifiable credentials needs.
type lru[K comparable, V any] struct {
	mu   sync.Mutex
	cap  int
	data map[K]V
	keys []K
}

func newLRU[K comparable, V any](capacity int) *lru[K, V] {
	return &lru[K, V]{cap: capacity, data: map[K]V{}}
}

func (l *lru[K, V]) peek(k K) (V, bool) {
	v, ok := l.data[k]
	return v, ok
}

func (l *lru[K, V]) put(k K, v V) {
	if _, ok := l.data[k]; !ok {
		if len(l.keys) >= l.cap {
			old := l.keys[0]
			l.keys = l.keys[1:]
			delete(l.data, old)
		}
		l.keys = append(l.keys, k)
	}
	l.data[k] = v
}

func (l *lru[K, V]) remove(k K) {
	delete(l.data, k)
}
