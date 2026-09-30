package acl

import (
	"fmt"
	"sync"
)

// Vpath is a share id paired with a path inside it. Ids stay plain int64
// throughout this package: the core imports the evaluator, never the reverse,
// so the evaluator cannot spell a core type without creating a cycle.
type Vpath struct {
	Share int64
	Path  Path
}

// Decision is one permission answer. By names the deciding grant, and is 0
// for the default-deny fallthrough, so a denial can always say which rule
// produced it or that no rule did.
type Decision struct {
	Allowed bool
	By      int64
}

// Evaluator holds the grant and membership table and answers questions
// against it. One mutex covers grants, membership and gen together; the
// decision cache carries its own.
type Evaluator struct {
	mu         sync.RWMutex
	grants     []Grant
	membership map[int64][]int64
	gen        int64

	cache *decisionCache
}

// NewEvaluator returns an evaluator with an empty table.
func NewEvaluator() *Evaluator {
	return &Evaluator{
		membership: map[int64][]int64{},
		cache:      newDecisionCache(decisionCacheCapacity),
	}
}

// LoadFromState replaces grants and memberships as one atomic swap: both
// tables are installed under a single lock acquisition and gen is bumped
// exactly once, so no caller observes new grants paired with old memberships
// under any generation the cache can have cached against.
//
// It carries no validation of its own. The write side validates a grant
// before a row exists, and the caller converts the store's row shape into
// these domain values; this package trusts what it is handed.
func (e *Evaluator) LoadFromState(grants []Grant, memberships []Membership) error {
	loaded := append([]Grant(nil), grants...)
	table := make(map[int64][]int64, len(memberships))
	for _, m := range memberships {
		table[m.User] = append(table[m.User], m.Group)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.grants = loaded
	e.membership = table
	e.gen++
	return nil
}

// ReplaceGrants installs a grant table on its own. It is the lower-level
// primitive: a caller that pairs it with SetMemberships gets a window where a
// concurrent Evaluate sees one half new and the other old, which is exactly
// what LoadFromState closes.
func (e *Evaluator) ReplaceGrants(grants []Grant) {
	loaded := append([]Grant(nil), grants...)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.grants = loaded
	e.gen++
}

// SetMemberships installs a membership table on its own, with the same
// narrower guarantee as ReplaceGrants.
func (e *Evaluator) SetMemberships(m map[int64][]int64) {
	table := make(map[int64][]int64, len(m))
	for user, groups := range m {
		table[user] = append([]int64(nil), groups...)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.membership = table
	e.gen++
}

// MembershipOf lists the groups containing a user.
func (e *Evaluator) MembershipOf(user int64) []int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	groups := e.membership[user]
	if len(groups) == 0 {
		return nil
	}
	return append([]int64(nil), groups...)
}

// Evaluate answers whether the user holds every bit of want at v. The answer
// is memoized against the generation it was computed under.
func (e *Evaluator) Evaluate(user int64, v Vpath, want Perms) Decision {
	key := cacheKey{user: user, share: v.Share, path: v.Path.String(), want: want}
	gen := e.genValue()
	if ok, d := e.cache.lookup(key, gen); ok {
		return d
	}
	d := e.evaluateUnlocked(user, v.Share, v.Path, want)
	e.cache.store(key, gen, d)
	return d
}

// Effective is the maximal bit set the user holds at v: one Evaluate call per
// single bit, in orderedBits order. Probing bit by bit is what makes
// composition across depths observable; a single multi-bit call stops at the
// first level that satisfies part of want.
func (e *Evaluator) Effective(user int64, v Vpath) Perms {
	var out Perms
	for _, bit := range orderedBits() {
		if e.Evaluate(user, v, bit).Allowed {
			out |= bit
		}
	}
	return out
}

func (e *Evaluator) genValue() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.gen
}

// evaluateUnlocked takes the read lock itself and runs the algorithm.
func (e *Evaluator) evaluateUnlocked(user, share int64, path Path, want Perms) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.evaluateLocked(user, share, path, want)
}

// evaluateLocked is the uncached algorithm, run with e.mu already held for
// reading.
//
// Depth decides first: the walk runs from the evaluated path's own depth down
// to the share root, and within one level a DENY of any wanted bit settles
// the answer before any ALLOW at that level is considered.
//
// Every ALLOW at a level then counts toward what is still wanted. Composition
// within a level is the same question as composition across levels: a caller
// holding read from one rule and download from another holds both, and there
// is no reading of "grant" under which the two rules cancel. Consulting only
// the first partially-covering ALLOW denied every operation that needs two
// bits to a caller whose bits came from a user grant and a group grant on the
// same folder, which is the ordinary shape of a shared team folder.
func (e *Evaluator) evaluateLocked(user, share int64, path Path, want Perms) Decision {
	candidates := e.matchingLocked(user, share, path)
	if len(candidates) == 0 {
		return Decision{}
	}
	for depth := path.Len(); depth >= 0; depth-- {
		level := levelAt(candidates, depth)
		if len(level) == 0 {
			continue
		}
		if g := findDeny(level, want); g != nil {
			return Decision{Allowed: false, By: g.ID}
		}
		// An ask that names no permission is asking whether the path is
		// reachable at all, which is how a caller resolves a name before it
		// knows what it will do with it. Any ALLOW covering this level
		// answers it. The loop below cannot: nothing intersects an empty set,
		// so every rule would be skipped and a question that was not about
		// permissions would come back denied.
		if want.IsEmpty() {
			if g := findAnyAllow(level); g != nil {
				return Decision{Allowed: true, By: g.ID}
			}
			for i := range level {
				if !level[i].Deny.IsEmpty() {
					return Decision{Allowed: false, By: 0}
				}
			}
			continue
		}
		for i := range level {
			if !level[i].Allow.Intersects(want) {
				continue
			}
			want = want.Remove(level[i].Allow)
			if want.IsEmpty() {
				// The rule that completed the set names the decision: a
				// caller asking why it was allowed gets the last rule it
				// needed rather than the first it matched.
				return Decision{Allowed: true, By: level[i].ID}
			}
		}
	}
	return Decision{}
}

// matchingLocked collects the grants that name the user on this share and
// apply to this path under the Inherit rule.
func (e *Evaluator) matchingLocked(user, share int64, path Path) []Grant {
	var out []Grant
	for _, g := range e.grants {
		if g.Share != share || !e.grantMatchesLocked(user, g) {
			continue
		}
		if g.Inherit {
			if g.Subpath.IsPrefixOf(path) {
				out = append(out, g)
			}
			continue
		}
		if subpathEquals(g.Subpath, path) {
			out = append(out, g)
		}
	}
	return out
}

// grantMatchesLocked reports whether the grant names this user, directly or
// through a group. A grant with neither set falls through to the membership
// scan, where a group of zero matches nothing.
func (e *Evaluator) grantMatchesLocked(user int64, g Grant) bool {
	if g.User != 0 {
		return g.User == user
	}
	for _, group := range e.membership[user] {
		if group == g.Group {
			return true
		}
	}
	return false
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

func findAnyAllow(level []Grant) *Grant {
	for i := range level {
		if !level[i].Allow.IsEmpty() {
			return &level[i]
		}
	}
	return nil
}

// RootEntry is one subtree a user has an explicit read-granting rule for.
//
// TrashEnabled, SharedExternally and BrokenReason are zero here. The core,
// which owns the share registry, fills them in per entry after calling Roots;
// this package has no registry to read them from.
type RootEntry struct {
	Label   string
	Share   int64
	Subpath Path
	Perms   Perms

	TrashEnabled     bool
	SharedExternally bool
	BrokenReason     string
}

// GeneratedRootLabel is the placeholder a root gets when its grant carries no
// label and names the share's whole root.
//
// Exported because the core replaces it with the share's own name: one
// spelling, so the substitution cannot miss by disagreeing about the format.
func GeneratedRootLabel(share int64) string {
	return fmt.Sprintf("share-%d", share)
}

// Roots lists one entry per grant record that names the user and whose Allow
// includes Read.
//
// Perms on each entry is the full effective set at the grant's own subpath,
// not the raw Allow bits of the grant that earned the listing, so an entry
// can appear here with Perms lacking Read when a deeper or same-depth DENY
// cancels it. Roots answers which subtrees have an explicit read-granting
// rule, never what the account can actually do; the gate for any operation
// is still Evaluate.
func (e *Evaluator) Roots(user int64) []RootEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()

	seen := map[string]int{}
	var out []RootEntry
	for _, g := range e.grants {
		if !e.grantMatchesLocked(user, g) || !g.Allow.Has(Read) {
			continue
		}
		base := g.Label
		if base == "" {
			if name := g.Subpath.Name(); name != "" {
				base = name
			} else {
				// The share's whole root with no label of its own. This layer
				// holds grants, not share definitions, so it cannot name the
				// folder: the id keeps the entry addressable and the core
				// substitutes the share's own name, which is what a person
				// recognises.
				base = GeneratedRootLabel(g.Share)
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

// effectiveLocked is Effective's already-locked twin, so Roots can hold the
// read lock across its whole walk without a nested acquisition.
func (e *Evaluator) effectiveLocked(user, share int64, path Path) Perms {
	var out Perms
	for _, bit := range orderedBits() {
		if e.evaluateLocked(user, share, path, bit).Allowed {
			out |= bit
		}
	}
	return out
}
