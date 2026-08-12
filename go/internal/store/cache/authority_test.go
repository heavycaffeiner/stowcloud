package cache_test

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
)

// collidingInos finds n inode numbers whose identities derive the same id at
// this width. Three is the smallest number that distinguishes recording both
// sides of a collision from recording only the newcomer: with two, the pair
// reproduces itself either way.
func collidingInos(t *testing.T, bits uint, n int) []uint64 {
	t.Helper()
	groups := map[cache.FileID][]uint64{}
	for ino := uint64(1); ino < 5_000; ino++ {
		id := cache.DeriveNarrow(cache.Ident{Share: testShare, Dev: testDev, Ino: ino}, 0, bits)
		groups[id] = append(groups[id], ino)
		if len(groups[id]) == n {
			return groups[id]
		}
	}
	t.Fatalf("no %d identities derive the same id at %d bits", n, bits)
	return nil
}

// collidingTree is a flat directory holding one file per inode, named so a
// permutation of the files is a permutation of the walk.
func collidingTree(inos []uint64) []entry {
	out := []entry{{path: "d", ino: 1_000_000, dir: true}}
	for i, ino := range inos {
		out = append(out, entry{path: "d/f" + strconv.Itoa(i), ino: ino})
	}
	return out
}

// walkOrders is every order the files can be walked in, with the directory
// kept in front because a parent gets its id before its children ask.
func walkOrders(entries []entry) [][]entry {
	var out [][]entry
	for _, perm := range permutations(entries[1:]) {
		out = append(out, slices.Concat(entries[:1], perm))
	}
	return out
}

// permutations is every ordering of s. Written as a plain recursion because the
// input here has three elements and four in one test.
func permutations(s []entry) [][]entry {
	if len(s) == 0 {
		return [][]entry{{}}
	}
	var out [][]entry
	for i := range s {
		rest := slices.Concat(s[:i], s[i+1:])
		for _, tail := range permutations(rest) {
			out = append(out, slices.Concat([]entry{s[i]}, tail))
		}
	}
	return out
}

// Three identities derive the same id, so two of the three are moved and both
// decisions depend on which file the first walk happened to reach first. The
// record has to make every one of those orders produce the same answer.
func TestThreeCollidingIdentitiesSurviveEveryWalkOrder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inos := collidingInos(t, collisionBits, 3)
	entries := collidingTree(inos)

	p := openPair(t, dir, collisionBits)
	first := populate(t, p.cache, entries)
	assertDistinct(t, first)

	overrides, err := p.state.CountFileIDOverrides(ctx)
	if err != nil {
		t.Fatalf("counting overrides: %v", err)
	}
	// Two moved files and the identity that kept the base id: three rows, and
	// the third is the one Phase 2 did not write.
	if overrides != 3 {
		t.Fatalf("a three-way collision recorded %d rows, want 3 (both sides plus the second move)",
			overrides)
	}

	for i, order := range walkOrders(entries) {
		p.dropCache(t)
		p = openPair(t, dir, collisionBits)
		got := populate(t, p.cache, order)
		for path, id := range first {
			if got[path] != id {
				t.Errorf("order %d: %s held %d and now holds %d", i, path, id, got[path])
			}
		}
		assertDistinct(t, got)

		again, cerr := p.state.CountFileIDOverrides(ctx)
		if cerr != nil {
			t.Fatalf("counting overrides: %v", cerr)
		}
		if again != overrides {
			t.Errorf("order %d changed the override count from %d to %d", i, overrides, again)
		}
	}
}

// A reservation is not a node row, and this is the case that tells them apart:
// the cache is empty, the identity that owns the base id has not been walked
// yet, and a newcomer that derives the same id must not take it.
func TestANewcomerCannotTakeAnAbsentOwnersReservedID(t *testing.T) {
	dir := t.TempDir()
	inos := collidingInos(t, collisionBits, 4)
	old, arrival := inos[:3], inos[3]

	p := openPair(t, dir, collisionBits)
	before := populate(t, p.cache, collidingTree(old))
	assertDistinct(t, before)

	// The newcomer is walked first, before any of the three that already hold
	// ids, and every one of those ids is currently held by nothing.
	p.dropCache(t)
	p = openPair(t, dir, collisionBits)
	entries := collidingTree(old)
	entries = slices.Insert(entries, 1, entry{path: "d/late", ino: arrival})
	after := populate(t, p.cache, entries)

	for path, id := range before {
		if after[path] != id {
			t.Errorf("%s held %d before the newcomer and %d after", path, id, after[path])
		}
	}
	assertDistinct(t, after)
}

func assertDistinct(t *testing.T, ids map[string]cache.FileID) {
	t.Helper()
	seen := make(map[cache.FileID]string, len(ids))
	for path, id := range ids {
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s both hold id %d", path, other, id)
		}
		seen[id] = path
	}
}
