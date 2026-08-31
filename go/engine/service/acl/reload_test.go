package acl

import (
	"context"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// Test 15. After a reload, Evaluate sees exactly the new table, never a mix.
func TestLoadFromStateReplacesBothTables(t *testing.T) {
	e := NewEvaluator()
	if err := e.LoadFromState(
		[]Grant{{ID: 1, Group: 3, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}},
		[]Membership{{User: 7, Group: 3}},
	); err != nil {
		t.Fatalf("load: %v", err)
	}
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 1)

	if err := e.LoadFromState(
		[]Grant{{ID: 2, Group: 4, Share: 1, Subpath: ParsePath("b"), Allow: Write, Inherit: true}},
		[]Membership{{User: 9, Group: 4}},
	); err != nil {
		t.Fatalf("reload: %v", err)
	}
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), false, 0)
	wantDecision(t, e.Evaluate(9, at(1, "b"), Write), true, 2)
	// The old membership paired with the new grant would allow this; the old
	// grant paired with the new membership would allow the reverse.
	wantDecision(t, e.Evaluate(7, at(1, "b"), Write), false, 0)
	wantDecision(t, e.Evaluate(9, at(1, "a"), Read), false, 0)
}

// LoadFromState copies what it is handed, so a caller mutating its own slice
// afterwards cannot rewrite the live grant table.
func TestLoadFromStateCopiesItsInput(t *testing.T) {
	grants := []Grant{userGrant(1, 7, "a", Read, 0)}
	e := NewEvaluator()
	if err := e.LoadFromState(grants, nil); err != nil {
		t.Fatalf("load: %v", err)
	}
	grants[0].Allow = 0
	grants[0].Deny = Read
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 1)
}

// Test 16. A concurrent Evaluate racing LoadFromState never sees new grants
// paired with old memberships or the reverse.
//
// Both tables allow user 7, but through different group ids, so either half
// of one table paired with either half of the other denies. A single
// evaluation answering "denied" is therefore a spliced read and nothing else,
// and the deciding grant id names which table answered.
func TestEvaluateRacingReloadNeverSplicesTheTwoTables(t *testing.T) {
	first := []Grant{{ID: 1, Group: 3, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}}
	firstMembers := []Membership{{User: 7, Group: 3}}
	second := []Grant{{ID: 2, Group: 4, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}}
	secondMembers := []Membership{{User: 7, Group: 4}}

	e := NewEvaluator()
	if err := e.LoadFromState(first, firstMembers); err != nil {
		t.Fatalf("load: %v", err)
	}

	const rounds = 200
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	task.Go(context.Background(), "acl reload loop", func() {
		defer wg.Done()
		for i := range rounds {
			var err error
			if i%2 == 0 {
				err = e.LoadFromState(second, secondMembers)
			} else {
				err = e.LoadFromState(first, firstMembers)
			}
			if err != nil {
				t.Errorf("reload: %v", err)
				break
			}
		}
		close(stop)
	})

	for range 4 {
		wg.Add(1)
		task.Go(context.Background(), "acl evaluate loop", func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Every answer must come whole from one table or the other.
				// A denial means the grant half and the membership half named
				// different group ids, which is the window under test.
				if got := e.Evaluate(7, at(1, "a"), Read); !got.Allowed || (got.By != 1 && got.By != 2) {
					t.Errorf("spliced answer %+v, want an allow from grant 1 or 2", got)
					return
				}
			}
		})
	}
	wg.Wait()
}

// Test 17. A decision cached before a reload is not returned after it, even
// for the identical key, and one that happens to match the new table is
// recomputed rather than reused past the generation boundary.
func TestCacheDoesNotSurviveAReload(t *testing.T) {
	e := NewEvaluator()
	if err := e.LoadFromState([]Grant{userGrant(1, 7, "a", Read, 0)}, nil); err != nil {
		t.Fatalf("load: %v", err)
	}
	q := at(1, "a")
	wantDecision(t, e.Evaluate(7, q, Read), true, 1)
	if cached, _ := e.cacheProbe(7, q, Read); !cached {
		t.Fatal("the first evaluation was not cached")
	}

	// A table whose answer differs.
	if err := e.LoadFromState([]Grant{userGrant(2, 7, "a", 0, Read)}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cached, d := e.cacheProbe(7, q, Read); cached {
		t.Fatalf("the pre-reload entry %+v is still reachable", d)
	}
	wantDecision(t, e.Evaluate(7, q, Read), false, 2)

	// A table whose answer is identical to the first: the entry must still be
	// a miss at the new generation, so the answer is recomputed.
	if err := e.LoadFromState([]Grant{userGrant(1, 7, "a", Read, 0)}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cached, d := e.cacheProbe(7, q, Read); cached {
		t.Fatalf("an entry from an earlier generation was reused: %+v", d)
	}
	wantDecision(t, e.Evaluate(7, q, Read), true, 1)
}

// The cache is bounded, so a client generating many distinct paths cannot
// grow it without limit.
func TestCacheIsBounded(t *testing.T) {
	e := loaded(t, userGrant(1, 7, "", Read, 0))
	for i := range decisionCacheCapacity * 2 {
		e.Evaluate(7, Vpath{Share: 1, Path: NewPath("d", string(rune('a'+i%26)), pathIndex(i))}, Read)
	}
	if got := e.cacheSize(); got > decisionCacheCapacity {
		t.Fatalf("cache holds %d entries, over the %d bound", got, decisionCacheCapacity)
	}
}

func pathIndex(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// The split primitives each bump the generation, so a caller using them
// directly still invalidates the cache; what they do not give is the atomic
// pairing LoadFromState provides.
func TestSplitPrimitivesBumpTheGeneration(t *testing.T) {
	e := NewEvaluator()
	start := e.generation()

	e.ReplaceGrants([]Grant{{ID: 1, Group: 3, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}})
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), false, 0)

	e.SetMemberships(map[int64][]int64{7: {3}})
	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 1)

	if e.generation() != start+2 {
		t.Fatalf("generation %d after two replacements, want %d", e.generation(), start+2)
	}
}

// SetMemberships copies the caller's map and its slices.
func TestSetMembershipsCopiesItsInput(t *testing.T) {
	e := NewEvaluator()
	e.ReplaceGrants([]Grant{{ID: 1, Group: 3, Share: 1, Subpath: ParsePath("a"), Allow: Read, Inherit: true}})

	table := map[int64][]int64{7: {3}}
	e.SetMemberships(table)
	table[8] = []int64{3}
	table[7][0] = 99

	wantDecision(t, e.Evaluate(7, at(1, "a"), Read), true, 1)
	wantDecision(t, e.Evaluate(8, at(1, "a"), Read), false, 0)
}

// Test 18. Many goroutines against a stationary table race-free, with the
// same answers a single goroutine gets.
func TestConcurrentReadsAgainstAStationaryTable(t *testing.T) {
	e := loaded(t,
		userGrant(1, 7, "a", Write, 0),
		userGrant(2, 7, "a/b", Read, 0),
		userGrant(3, 7, "a/b/secret", 0, Read),
	)
	q := at(1, "a/b/c")
	wantEffective := e.Effective(7, q)
	wantRoots := e.Roots(7)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		task.Go(context.Background(), "acl stationary read loop", func() {
			defer wg.Done()
			for range 100 {
				if got := e.Effective(7, q); got != wantEffective {
					t.Errorf("effective %s, want %s", got, wantEffective)
					return
				}
				if got := e.Roots(7); len(got) != len(wantRoots) {
					t.Errorf("roots length %d, want %d", len(got), len(wantRoots))
					return
				}
				e.Evaluate(7, at(1, "a/b/secret"), Read)
			}
		})
	}
	wg.Wait()
}
