package acl

import (
	"fmt"
	"sync"
)

// The evaluation algorithm:
// depth-first from the deepest matching grant down to the share root;
// same-depth DENY beats ALLOW; a deeper ALLOW beats a shallower DENY; there
// is no principal-kind priority, so ties are broken by depth alone; and the
// default is deny. A multi-bit want is satisfied by composition across
// depths, so READ at /a/b plus WRITE at /a together grant READ|WRITE at /a/b.

// Decision is the outcome of one evaluation. The deciding grant id is
// carried even where the caller only branches on Allowed, so every denial
// stays explainable in the API and the audit log.
type Decision struct {
	Allowed bool
	By      int64 // the grant that decided, 0 for the default-deny fallthrough
}

// Evaluator answers what a user may do at a path. Grants and memberships are
// wholesale-replaced behind a generation counter, which invalidates every
// cached decision without a sweep: a change is a swap, and anything cached
// under an older generation is simply not found.
type Evaluator struct {
	mu         sync.RWMutex
	grants     []Grant
	membership map[int64][]int64
	gen        int64

	// cache is bounded; FIFO eviction keeps the attack surface of an
	// attacker-generated path set from growing memory.
	cache *decisionCache
}

type cacheKey struct {
	user, share int64
	path        string
	want        Perms
}

type cacheEntry struct {
	gen int64
	d   Decision
}

func NewEvaluator() *Evaluator {
	return &Evaluator{
		membership: map[int64][]int64{},
		cache:      newDecisionCache(4096),
	}
}

// ReplaceGrants swaps the whole grant set and bumps the counter that makes
// every previous decision stale.
func (e *Evaluator) ReplaceGrants(grants []Grant) {
	e.mu.Lock()
	e.grants = grants
	e.mu.Unlock()
	e.bump()
}

// SetMemberships swaps the whole membership map and bumps the counter.
func (e *Evaluator) SetMemberships(m membership) {
	e.mu.Lock()
	e.membership = m
	e.mu.Unlock()
	e.bump()
}

func (e *Evaluator) bump() {
	e.mu.Lock()
	e.gen++
	e.mu.Unlock()
}

// MembershipOf returns the groups a user belongs to.
func (e *Evaluator) MembershipOf(user int64) []int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.membership[user]
}

// Vpath is the ACL-level virtual path: a share and a share-relative path.
type Vpath struct {
	Share int64
	Path  Path
}

// Evaluate resolves what user may do at vpath. It returns a zero Perms and
// no error for a path outside every grant: "you may do nothing here" and
// "there is nothing here" are the same answer by design, and the HTTP layer
// turns both into 404 (S2: existence is never revealed).
func (e *Evaluator) Evaluate(user int64, v Vpath, want Perms) Decision {
	key := cacheKey{user: user, share: v.Share, path: v.Path.String(), want: want}
	gen := e.genValue()
	if ok, d := e.cache.lookup(key, gen); ok {
		return d
	}
	d := e.evaluate(user, v.Share, v.Path, want)
	e.cache.store(key, gen, d)
	return d
}

func (e *Evaluator) genValue() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.gen
}

// Effective is the maximal set of bits user holds at vpath: the OR of each
// single-bit Evaluate answer.
func (e *Evaluator) Effective(user int64, v Vpath) Perms {
	var out Perms
	for _, bit := range orderedBits() {
		if e.Evaluate(user, v, bit).Allowed {
			out |= bit
		}
	}
	return out
}

// grantsFor collects the grants that could match, the raw depth-first
// working set.
func (e *Evaluator) matching(user int64, share int64, path Path) []Grant {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Grant
	for _, g := range e.grants {
		if g.Share != share || !e.grantMatches(user, g) {
			continue
		}
		if g.Inherit {
			if g.Subpath.IsPrefixOf(path) {
				out = append(out, g)
			}
		} else if subpathEquals(g.Subpath, path) {
			out = append(out, g)
		}
	}
	return out
}

func (e *Evaluator) grantMatches(user int64, g Grant) bool {
	if g.User != 0 {
		return g.User == user
	}
	for _, m := range e.membership[user] {
		if m == g.Group {
			return true
		}
	}
	return false
}

// evaluate implements §4.3.9's algorithm over the deepest-first levels.
func (e *Evaluator) evaluate(user int64, share int64, path Path, want Perms) Decision {
	candidates := e.matching(user, share, path)
	if len(candidates) == 0 {
		return Decision{Allowed: false, By: 0}
	}
	maxDepth := path.Len()
	for depth := maxDepth; depth >= 0; depth-- {
		level := levelAt(candidates, depth)
		if len(level) == 0 {
			continue
		}
		if g := findDeny(level, want); g != nil {
			return Decision{Allowed: false, By: g.ID}
		}
		if g := findAllowAll(level, want); g != nil {
			return Decision{Allowed: true, By: g.ID}
		}
		if g := findAllowAny(level, want); g != nil {
			want = want.Remove(g.Allow)
			if want.IsEmpty() {
				return Decision{Allowed: true, By: g.ID}
			}
		}
	}
	return Decision{Allowed: false, By: 0}
}

func levelAt(gs []Grant, depth int) []Grant {
	var out []Grant
	for _, g := range gs {
		if g.Subpath.Len() == depth {
			out = append(out, g)
		}
	}
	return out
}

func findDeny(level []Grant, want Perms) *Grant {
	for i := range level {
		if level[i].Deny.Intersects(want) {
			return &level[i]
		}
	}
	return nil
}

func findAllowAll(level []Grant, want Perms) *Grant {
	for i := range level {
		if level[i].Allow.Has(want) {
			return &level[i]
		}
	}
	return nil
}

func findAllowAny(level []Grant, want Perms) *Grant {
	for i := range level {
		if level[i].Allow.Intersects(want) {
			return &level[i]
		}
	}
	return nil
}

// RootEntry is one entry of the virtual root: a share the user can read, under
// its display label. The core resolves a virtual path by finding the entry
// whose label matches, without re-reading the grant table.
//
// The two flags at the bottom are filled in by the caller that owns the share
// registry (the core), which is why they are carried here rather than looked
// up: a client's root listing needs to render the "shared with another
// service" badge and the "this delete is undoable" warning, and re-deriving
// them means a second walk of the share table.
type RootEntry struct {
	Label   string
	Share   int64
	Subpath Path
	Perms   Perms

	TrashEnabled     bool
	SharedExternally bool
}

// Roots is the virtual root projection for user: one entry per distinct
// READ-granted rule, labeled, with label collisions given a " (2)", " (3)"
// suffix in encounter order. It is what the core's one resolver looks up a
// virtual path's share by.
func (e *Evaluator) Roots(user int64) []RootEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()

	seen := map[string]int{}
	var out []RootEntry
	for _, g := range e.grants {
		if !e.grantMatches(user, g) || !g.Allow.Has(Read) {
			continue
		}
		base := g.Label
		if base == "" {
			if name := g.Subpath.Name(); name != "" {
				base = name
			} else {
				base = fmt.Sprintf("share-%d", g.Share)
			}
		}
		seen[base]++
		label := base
		if n := seen[base]; n > 1 {
			label = fmt.Sprintf("%s (%d)", base, n)
		}
		out = append(out, RootEntry{
			Label:   label,
			Share:   g.Share,
			Subpath: g.Subpath,
			Perms:   e.effectiveLocked(user, g.Share, g.Subpath),
		})
	}
	return out
}

// effectiveLocked computes the maximal perms at path using the read lock
// already held, which is the depth-first single-bit OR.
func (e *Evaluator) effectiveLocked(user, share int64, path Path) Perms {
	var out Perms
	for _, bit := range orderedBits() {
		if e.evaluate(user, share, path, bit).Allowed {
			out |= bit
		}
	}
	return out
}

// membership is the user-to-groups map.
type membership map[int64][]int64
