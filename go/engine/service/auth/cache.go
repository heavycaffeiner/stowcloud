package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// The three tiers of the verification path.
//
// They exist to bound how often the memory-hard function runs, not to weaken
// what it protects. All three live and die with the process and are named by
// per-process random material, so a memory dump yields nothing that can be
// attacked offline. All three carry the generation they were filled under, so
// a credential change invalidates every entry by comparison rather than by
// being found and deleted.
const (
	// Tier 1 is the connection memo, keyed by the digest of a presented
	// header. Tier 3 is the app-password bypass. Neither has a lifetime of
	// its own beyond the generation counter and its own short window.
	connMemoCapacity = 4096
	tokenCacheCap    = 1024
	credCacheCap     = 4096

	// Tier 2 holds password decisions. A positive one lives fifteen minutes
	// absolute and five idle, so an active sync stays warm and an abandoned
	// client's entries evaporate.
	credPositiveTTL  = 15 * time.Minute
	credPositiveIdle = 5 * time.Minute

	// A negative one lives thirty seconds, and that is load-bearing: a client
	// looping with a wrong password would otherwise buy a full Argon2
	// invocation per attempt, which is a denial of service arriving as
	// ordinary traffic.
	credNegativeTTL = 30 * time.Second

	// App passwords carry 256 bits of entropy, so a verified one may skip the
	// memory-hard path entirely. The short window bounds the revocation delay
	// this adds beyond the generation check.
	tokenTTL = 60 * time.Second
)

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
	scope     Scope
	gen       int64
	inserted  time.Time
}

// caches bundles the three tiers together with the ephemeral key naming tier-2
// entries.
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
		// A cache that names its entries with a predictable key is worse than
		// no cache, and a system whose random source has failed should not
		// continue on a guess.
		panic("the system random source failed while seeding the credential cache: " + err.Error())
	}
	return &caches{
		ephemeral:  key,
		credential: newLRU[[16]byte, credEntry](credCacheCap),
		connMemo:   newLRU[[32]byte, connEntry](connMemoCapacity),
		token:      newLRU[[32]byte, tokenEntry](tokenCacheCap),
		clk:        clk,
	}
}

// credKey names one account-and-password pair: an HMAC under the per-process
// key, never a hash of the password on its own.
//
// It reports false when the key could not be derived, and the caller then
// takes the uncached path. A hash never fails a write in practice; the
// alternative to this branch is discarding an error, and a lookup that
// silently used a partial key would cache a decision under a name no later
// lookup could reproduce.
func (c *caches) credKey(user string, pw []byte) ([16]byte, bool) {
	var out [16]byte
	mac := hmac.New(sha256.New, c.ephemeral[:])
	input := make([]byte, 0, len("cred\x00")+len(user)+1+len(pw))
	input = append(input, "cred\x00"...)
	input = append(input, user...)
	input = append(input, 0)
	input = append(input, pw...)
	if _, err := mac.Write(input); err != nil {
		return out, false
	}
	copy(out[:], mac.Sum(nil)[:16])
	return out, true
}

// credLookup returns a cached decision, or false for a miss and for an entry
// a generation or a window has made stale.
func (c *caches) credLookup(key [16]byte, gen int64) (Outcome, bool) {
	c.credential.mu.Lock()
	defer c.credential.mu.Unlock()

	e, present := c.credential.peek(key)
	if !present || e.gen != gen {
		if present {
			c.credential.remove(key)
		}
		return Outcome{}, false
	}
	now := c.clk.Now()
	if e.outcome.Accepted {
		if now.Sub(e.inserted) > credPositiveTTL || now.Sub(e.lastHit) > credPositiveIdle {
			c.credential.remove(key)
			return Outcome{}, false
		}
		e.lastHit = now
		c.credential.put(key, e)
		return e.outcome, true
	}
	if now.Sub(e.inserted) > credNegativeTTL {
		c.credential.remove(key)
		return Outcome{}, false
	}
	return e.outcome, true
}

func (c *caches) credStore(key [16]byte, o Outcome, gen int64) {
	c.credential.mu.Lock()
	defer c.credential.mu.Unlock()
	now := c.clk.Now()
	c.credential.put(key, credEntry{outcome: o, gen: gen, inserted: now, lastHit: now})
}

// connLookup is the connection memo: the principal a presented header already
// resolved to, invalidated by nothing but the generation counter.
func (c *caches) connLookup(hash [32]byte, gen int64) (Principal, bool) {
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

func (c *caches) connStore(hash [32]byte, p Principal, gen int64) {
	c.connMemo.mu.Lock()
	defer c.connMemo.mu.Unlock()
	c.connMemo.put(hash, connEntry{principal: p, gen: gen})
}

// tokenLookup is the app-password bypass.
func (c *caches) tokenLookup(hash [32]byte, gen int64) (Principal, Scope, bool) {
	c.token.mu.Lock()
	defer c.token.mu.Unlock()
	e, present := c.token.peek(hash)
	if !present {
		return Principal{}, Scope{}, false
	}
	if e.gen != gen || c.clk.Now().Sub(e.inserted) > tokenTTL {
		c.token.remove(hash)
		return Principal{}, Scope{}, false
	}
	return e.principal, e.scope, true
}

func (c *caches) tokenStore(hash [32]byte, p Principal, scope Scope, gen int64) {
	c.token.mu.Lock()
	defer c.token.mu.Unlock()
	c.token.put(hash, tokenEntry{principal: p, scope: scope, gen: gen, inserted: c.clk.Now()})
}

// lru is a bounded map evicted in insertion order, guarded by its own mutex.
// Access-order eviction would take a linked list to do properly; insertion
// order keeps the bound that stops an attacker filling memory, which is all a
// cache of re-verifiable decisions needs.
type lru[K comparable, V any] struct {
	mu   sync.Mutex
	cap  int
	data map[K]V
	keys []K
}

func newLRU[K comparable, V any](capacity int) *lru[K, V] {
	return &lru[K, V]{cap: capacity, data: make(map[K]V, capacity)}
}

func (l *lru[K, V]) peek(k K) (V, bool) {
	v, ok := l.data[k]
	return v, ok
}

func (l *lru[K, V]) put(k K, v V) {
	if _, ok := l.data[k]; !ok {
		if len(l.keys) >= l.cap {
			delete(l.data, l.keys[0])
			l.keys = l.keys[1:]
		}
		l.keys = append(l.keys, k)
	}
	l.data[k] = v
}

func (l *lru[K, V]) remove(k K) { delete(l.data, k) }
