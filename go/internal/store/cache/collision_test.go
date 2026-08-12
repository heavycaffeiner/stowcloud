package cache_test

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
)

// collisionBits folds the derivation into a width where a hundred files
// collide, which is what makes the collision path reachable at all. At the
// width the product uses it takes roughly three billion files.
const collisionBits = 8

// crowd is a flat directory with more files in it than the narrowed
// derivation has ids.
func crowd() []entry {
	out := []entry{{path: "d", ino: 1, dir: true, btime: btime(1)}}
	for i := range 100 {
		out = append(out, entry{
			path:  "d/f" + strconv.Itoa(i),
			ino:   uint64(100 + i),
			btime: btime(int64(i)),
		})
	}
	return out
}

// Two files that derive the same id must never be conflated: one keeps the
// derived id, the other is moved, and which one moved is recorded rather than
// recomputed.
func TestForcedCollisionRecordsAnOverride(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := openPair(t, dir, collisionBits)

	before := populate(t, p.cache, crowd())

	overrides, err := p.state.CountFileIDOverrides(ctx)
	if err != nil {
		t.Fatalf("counting overrides: %v", err)
	}
	if overrides == 0 {
		t.Fatalf("%d files at %d bits recorded no override: the collision path did not run",
			len(crowd()), collisionBits)
	}

	seen := make(map[cache.FileID]string, len(before))
	for path, id := range before {
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s both hold id %d", path, other, id)
		}
		seen[id] = path
	}

	// The recorded decision, not the derivation, is what a rebuild reproduces:
	// which file took the base id depended on which was inserted first, and
	// this walk goes the other way.
	p.dropCache(t)
	rebuilt := openPair(t, dir, collisionBits)
	reversed := slices.Clone(crowd())
	slices.Reverse(reversed)
	// The directory still has to be seen before what is inside it.
	slices.SortStableFunc(reversed, func(a, b entry) int { return depthOf(a.path) - depthOf(b.path) })
	after := populate(t, rebuilt.cache, reversed)

	for path, id := range before {
		if after[path] != id {
			t.Errorf("%s: id %d before the rebuild and %d after", path, id, after[path])
		}
	}

	// A rebuild that reads the record does not write a new one.
	again, err := rebuilt.state.CountFileIDOverrides(ctx)
	if err != nil {
		t.Fatalf("counting overrides after the rebuild: %v", err)
	}
	if again != overrides {
		t.Errorf("the rebuild changed the override count from %d to %d", overrides, again)
	}
}

// The override is consulted before anything is derived, so a recorded decision
// survives even a change to the derivation itself.
func TestOverrideOutranksTheDerivation(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)

	ident := cache.Ident{Share: testShare, Dev: testDev, Ino: 9, Btime: btime(9)}
	const forced cache.FileID = 424242

	if cache.DeriveID(ident, 0) == forced {
		t.Fatal("the derivation happens to agree with the forced id, which makes this test prove nothing")
	}
	if err := p.state.RecordFileIDs(ctx, cache.Assignment{Ident: ident, ID: forced}); err != nil {
		t.Fatalf("recording an override: %v", err)
	}

	got, ok, err := p.state.LookupFileID(ctx, ident)
	if err != nil || !ok {
		t.Fatalf("reading back the override: %v (found %v)", err, ok)
	}
	if got != forced {
		t.Fatalf("the override read back as %d, want %d", got, forced)
	}

	ids := populate(t, p.cache, []entry{{path: "f", ino: 9, btime: btime(9)}})
	if ids["f"] != forced {
		t.Errorf("the allocation returned %d, want the recorded %d", ids["f"], forced)
	}
}

// An absent birth time and a zero one are different rows in the override
// table, which a nullable column in a WITHOUT ROWID primary key could not hold
// at all.
func TestOverrideKeepsAbsentAndZeroBirthTimeApart(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)

	zero := int64(0)
	absent := cache.Ident{Share: testShare, Dev: 5, Ino: 5}
	present := cache.Ident{Share: testShare, Dev: 5, Ino: 5, Btime: &zero}

	if err := p.state.RecordFileIDs(ctx, cache.Assignment{Ident: absent, ID: 111}); err != nil {
		t.Fatalf("recording the absent one: %v", err)
	}
	if err := p.state.RecordFileIDs(ctx, cache.Assignment{Ident: present, ID: 222}); err != nil {
		t.Fatalf("recording the zero one: %v", err)
	}

	for _, tc := range []struct {
		name  string
		ident cache.Ident
		want  cache.FileID
	}{
		{"absent", absent, 111},
		{"zero", present, 222},
	} {
		got, ok, err := p.state.LookupFileID(ctx, tc.ident)
		if err != nil || !ok {
			t.Fatalf("%s: %v (found %v)", tc.name, err, ok)
		}
		if got != tc.want {
			t.Errorf("%s read back as %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A file whose identity has never been seen has no override, and asking is not
// an error.
func TestNoOverrideIsNotAnError(t *testing.T) {
	p := openPair(t, t.TempDir(), 0)
	_, ok, err := p.state.LookupFileID(context.Background(),
		cache.Ident{Share: testShare, Dev: 1, Ino: 1})
	if err != nil {
		t.Fatalf("looking up an identity with no override: %v", err)
	}
	if ok {
		t.Error("an identity nothing recorded came back with an override")
	}
}
