package cache_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

const (
	testShare vfs.ShareID = 7

	// testDev is what every synthetic stat in these tests reports, so an
	// identity built by hand and one built from a walk are the same file.
	testDev uint64 = 0x40_0001
)

// pair is the two databases the allocation spans: the node rows in the
// rebuildable half and the collision record in the durable one.
type pair struct {
	dir   string
	cf    *dbfile.DB
	sf    *dbfile.DB
	cache *cache.DB
	state *state.DB
}

// openPair opens both databases under dir. bits is the derivation width, and
// zero means the one the product uses.
func openPair(t *testing.T, dir string, bits uint) *pair {
	t.Helper()
	ctx := context.Background()

	sf, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening state.db: %v", err)
	}
	cf, err := dbfile.Open(ctx, cache.Spec(filepath.Join(dir, "cache.db")))
	if err != nil {
		t.Fatalf("opening cache.db: %v", err)
	}

	st := state.New(sf)
	c := cache.New(cf, st)
	if bits != 0 {
		c = cache.NewNarrow(cf, st, bits)
	}
	p := &pair{dir: dir, cf: cf, sf: sf, cache: c, state: st}
	t.Cleanup(func() { p.close(t) })
	return p
}

func (p *pair) close(t *testing.T) {
	t.Helper()
	if err := p.cf.Close(); err != nil {
		t.Errorf("closing cache.db: %v", err)
	}
	if err := p.sf.Close(); err != nil {
		t.Errorf("closing state.db: %v", err)
	}
}

// dropCache does what an operator does when principle 1 is taken at its word.
func (p *pair) dropCache(t *testing.T) {
	t.Helper()
	if err := p.cf.Close(); err != nil {
		t.Fatalf("closing cache.db: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(p.dir, "cache.db"+suffix)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("deleting cache.db%s: %v", suffix, err)
		}
	}
}

// entry is one file in the synthetic tree. depth is what an insertion order
// has to respect: a parent gets its id before its children ask for theirs.
type entry struct {
	path  string
	ino   uint64
	dir   bool
	btime *int64
}

func btime(ns int64) *int64 { return &ns }

// tree is a share with two directory levels, a file with no birth time, and
// two files that share a birth time. Every one of those is a case the
// derivation has to keep apart.
func tree() []entry {
	return []entry{
		{path: "a", ino: 10, dir: true, btime: btime(1_000)},
		{path: "b", ino: 11, dir: true, btime: btime(1_000)},
		{path: "no-btime", ino: 12},
		{path: "zero-btime", ino: 13, btime: btime(0)},
		{path: "a/x", ino: 14, dir: true, btime: btime(2_000)},
		{path: "a/y", ino: 15, dir: true},
		{path: "b/z", ino: 16, dir: true, btime: btime(2_000)},
		{path: "a/f1", ino: 17, btime: btime(3_000)},
		{path: "a/x/f2", ino: 18, btime: btime(3_000)},
		{path: "b/z/f3", ino: 19, btime: btime(4_000)},
		{path: "a/y/f4", ino: 20},
	}
}

func (e entry) stat() vfs.Stat {
	st := vfs.Stat{
		Dev:     testDev,
		Ino:     e.ino,
		BtimeNs: e.btime,
		MtimeNs: 5_000,
		Size:    e.ino * 3,
		Kind:    vfs.KindFile,
	}
	if e.dir {
		st.Kind = vfs.KindDir
		st.Size = 4096
	}
	return st
}

func parentOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

func nameOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// depthOf counts separators, which is the only ordering constraint a walk has:
// a directory is seen before what is inside it.
func depthOf(path string) int { return strings.Count(path, "/") }

// populate walks the entries in the given order and records the id each path
// came back with.
func populate(t *testing.T, c *cache.DB, entries []entry) map[string]cache.FileID {
	t.Helper()
	ctx := context.Background()
	ids := map[string]cache.FileID{"": cache.RootID}

	if err := c.Write(ctx, func(tx *sql.Tx) error {
		for _, e := range entries {
			parent, ok := ids[parentOf(e.path)]
			if !ok {
				t.Fatalf("%s was walked before its parent", e.path)
			}
			id, err := c.Upsert(ctx, tx, testShare, parent, nameOf(e.path), e.stat())
			if err != nil {
				return err
			}
			ids[e.path] = id
		}
		return nil
	}); err != nil {
		t.Fatalf("populating: %v", err)
	}
	delete(ids, "")
	return ids
}

// shallowFirst is a walk order. reverse flips the order within each level,
// which is what a second walk of the same tree can honestly return: readdir
// order is not stable across a rebuild.
func shallowFirst(entries []entry, reverse bool) []entry {
	out := slices.Clone(entries)
	slices.SortStableFunc(out, func(a, b entry) int { return depthOf(a.path) - depthOf(b.path) })
	if !reverse {
		return out
	}
	slices.Reverse(out)
	slices.SortStableFunc(out, func(a, b entry) int { return depthOf(a.path) - depthOf(b.path) })
	return out
}

// The whole point of the derivation, executed: delete the cache, walk the same
// tree in a different order, and every file keeps the id its sync clients
// already hold.
func TestRebuildProducesTheSameIDs(t *testing.T) {
	dir := t.TempDir()
	p := openPair(t, dir, 0)

	before := populate(t, p.cache, shallowFirst(tree(), false))
	if len(before) != len(tree()) {
		t.Fatalf("populated %d paths, want %d", len(before), len(tree()))
	}

	p.dropCache(t)
	rebuilt := openPair(t, dir, 0)
	after := populate(t, rebuilt.cache, shallowFirst(tree(), true))

	for path, id := range before {
		if after[path] != id {
			t.Errorf("%s: id %d before the rebuild and %d after", path, id, after[path])
		}
	}
}

// A rebuild that reuses the same ids is only worth anything if the ids were
// distinct in the first place.
func TestDerivedIDsAreDistinctAcrossTheTree(t *testing.T) {
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, shallowFirst(tree(), false))

	seen := make(map[cache.FileID]string, len(ids))
	for path, id := range ids {
		if id <= 0 {
			t.Errorf("%s got id %d, which is the no-id sentinel or negative", path, id)
		}
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s both got id %d", path, other, id)
		}
		seen[id] = path
	}
}

// An absent birth time and a birth time of zero are different facts, and a
// filesystem that reports neither must not collapse two files into one id.
func TestAbsentAndZeroBirthTimeDeriveDifferentIDs(t *testing.T) {
	zero := int64(0)
	absent := cache.Ident{Share: testShare, Dev: 1, Ino: 2}
	present := cache.Ident{Share: testShare, Dev: 1, Ino: 2, Btime: &zero}

	if cache.DeriveID(absent, 0) == cache.DeriveID(present, 0) {
		t.Fatal("a file with no birth time and one with a zero birth time derived the same id")
	}
	if absent.Equal(present) {
		t.Error("Ident.Equal conflated an absent birth time with a zero one")
	}
}

// Every part of the identity tuple is load-bearing, and the attempt counter is
// what makes a second derivation available at all.
func TestEveryFieldChangesTheID(t *testing.T) {
	base := cache.Ident{Share: 1, Dev: 2, Ino: 3, Btime: btime(4)}
	id := cache.DeriveID(base, 0)

	for name, other := range map[string]cache.Ident{
		"share": {Share: 2, Dev: 2, Ino: 3, Btime: btime(4)},
		"dev":   {Share: 1, Dev: 3, Ino: 3, Btime: btime(4)},
		"ino":   {Share: 1, Dev: 2, Ino: 4, Btime: btime(4)},
		"btime": {Share: 1, Dev: 2, Ino: 3, Btime: btime(5)},
	} {
		if cache.DeriveID(other, 0) == id {
			t.Errorf("changing %s did not change the id", name)
		}
	}
	if cache.DeriveID(base, 1) == id {
		t.Error("a second attempt derived the same id as the first")
	}
}

// The id reaches a sync client as a signed 64-bit integer, and zero is the
// property emitter's "no id".
func TestIDsAreNeverZeroAndAlwaysPositive(t *testing.T) {
	for ino := uint64(0); ino < 20_000; ino++ {
		id := cache.DeriveID(cache.Ident{Share: 1, Dev: 1, Ino: ino}, 0)
		if id <= 0 {
			t.Fatalf("ino %d derived %d", ino, id)
		}
	}
}

// The derivation is a wire contract in one direction: every id a client holds
// came out of it. A change to the key layout, the prefix or the hash has to be
// deliberate, so it is pinned here.
func TestDerivationIsPinned(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ident cache.Ident
		want  cache.FileID
	}{
		{"no birth time", cache.Ident{Share: 1, Dev: 2, Ino: 3}, 8682140122616183347},
		{"birth time", cache.Ident{Share: 1, Dev: 2, Ino: 3, Btime: btime(4)}, 4329706949728033163},
	} {
		if got := cache.DeriveID(tc.ident, 0); got != tc.want {
			t.Errorf("%s: DeriveID = %d, want %d", tc.name, got, tc.want)
		}
	}
}
