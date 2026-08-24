package cache_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
)

// resolve is the assertion the parent-chain walk exists for.
func resolve(t *testing.T, c *cache.DB, id cache.FileID) string {
	t.Helper()
	share, path, err := c.Resolve(context.Background(), id)
	if err != nil {
		t.Fatalf("resolving %d: %v", id, err)
	}
	if share != testShare {
		t.Errorf("node %d resolved to share %d, want %d", id, share, testShare)
	}
	return path.String()
}

func TestResolveWalksToTheShareRoot(t *testing.T) {
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, shallowFirst(tree(), false))

	for _, path := range []string{"a", "a/x", "a/x/f2", "b/z/f3", "no-btime"} {
		if got := resolve(t, p.cache, ids[path]); got != path {
			t.Errorf("node for %s resolved to %q", path, got)
		}
	}
}

func TestResolveUnknownIDIsNotAnError(t *testing.T) {
	p := openPair(t, t.TempDir(), 0)
	_, _, err := p.cache.Resolve(context.Background(), 123456789)
	if !errors.Is(err, cache.ErrNoNode) {
		t.Fatalf("resolving an id nothing allocated returned %v, want ErrNoNode", err)
	}
}

// Renaming a directory is one row. Everything underneath keeps referencing its
// parent by id, so the whole subtree resolves to the new path with no further
// writes, which is what the missing path column buys.
func TestRenameMovesTheSubtreeWithOneUpdate(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, shallowFirst(tree(), false))

	if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		return p.cache.Rename(ctx, tx, ids["a/x"], ids["b"], "moved")
	}); err != nil {
		t.Fatalf("renaming: %v", err)
	}

	if got := resolve(t, p.cache, ids["a/x"]); got != "b/moved" {
		t.Errorf("the renamed directory resolved to %q, want b/moved", got)
	}
	if got := resolve(t, p.cache, ids["a/x/f2"]); got != "b/moved/f2" {
		t.Errorf("the file underneath resolved to %q, want b/moved/f2", got)
	}
}

func TestRenamingSomethingWithNoRowSaysSo(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)
	err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		return p.cache.Rename(ctx, tx, 999, cache.RootID, "x")
	})
	if !errors.Is(err, cache.ErrNoNode) {
		t.Fatalf("renaming an id nothing allocated returned %v, want ErrNoNode", err)
	}
}

// The filesystem is the source of truth and this is the cache catching up: a
// file moved by something else keeps its id and gets its new place recorded
// the next time it is seen.
func TestUpsertFollowsAnOutOfBandMove(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, shallowFirst(tree(), false))

	moved := entry{path: "b/f1", ino: 17, btime: btime(3_000)}
	var got cache.FileID
	if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		var err error
		got, err = p.cache.Upsert(ctx, tx, testShare, ids["b"], "f1", moved.stat())
		return err
	}); err != nil {
		t.Fatalf("re-observing a moved file: %v", err)
	}

	if got != ids["a/f1"] {
		t.Errorf("the moved file got id %d, want the %d it already had", got, ids["a/f1"])
	}
	if path := resolve(t, p.cache, got); path != "b/f1" {
		t.Errorf("the moved file resolves to %q, want b/f1", path)
	}
}

func TestLookupDoesNotAllocate(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)

	st := entry{path: "lonely", ino: 500, btime: btime(1)}.stat()
	if _, ok, err := p.cache.Lookup(ctx, testShare, st); err != nil || ok {
		t.Fatalf("Lookup of an unseen file: found %v, err %v", ok, err)
	}

	ids := populate(t, p.cache, []entry{{path: "lonely", ino: 500, btime: btime(1)}})
	id, ok, err := p.cache.Lookup(ctx, testShare, st)
	if err != nil || !ok {
		t.Fatalf("Lookup after Upsert: found %v, err %v", ok, err)
	}
	if id != ids["lonely"] {
		t.Errorf("Lookup returned %d, Upsert returned %d", id, ids["lonely"])
	}
}

func TestDiretagIsStoredReadBackAndInvalidated(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, shallowFirst(tree(), false))
	dir := ids["a"]

	if _, ok, err := p.cache.DirEtag(ctx, testShare, dir); err != nil || ok {
		t.Fatalf("a directory with no cached aggregate: found %v, err %v", ok, err)
	}

	want := cache.Aggregate{Etag: "abc123", RSize: 4096, RCount: 3}
	if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		return p.cache.PutDirEtag(ctx, tx, testShare, dir, want, 0)
	}); err != nil {
		t.Fatalf("storing an aggregate: %v", err)
	}

	got, ok, err := p.cache.DirEtag(ctx, testShare, dir)
	if err != nil || !ok {
		t.Fatalf("reading back an aggregate: found %v, err %v", ok, err)
	}
	if got != want {
		t.Errorf("read back %+v, want %+v", got, want)
	}

	if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		return p.cache.MarkDirty(ctx, tx, testShare, []cache.FileID{dir, ids["a/x"]})
	}); err != nil {
		t.Fatalf("invalidating: %v", err)
	}
	if _, ok, err := p.cache.DirEtag(ctx, testShare, dir); err != nil || ok {
		t.Fatalf("a dirty aggregate came back as usable: found %v, err %v", ok, err)
	}

	// A directory that had no row at all takes a placeholder, so asking about
	// it answers "recompute" rather than failing.
	if _, ok, err := p.cache.DirEtag(ctx, testShare, ids["a/x"]); err != nil || ok {
		t.Fatalf("the placeholder read as usable: found %v, err %v", ok, err)
	}
}

// The share generation is the whole-share invalidation that touches no rows.
func TestBumpingTheShareGenerationInvalidatesEveryAggregate(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, shallowFirst(tree(), false))

	gen, err := p.cache.ShareGen(ctx, testShare)
	if err != nil {
		t.Fatalf("reading the generation: %v", err)
	}
	if gen != 0 {
		t.Fatalf("a share nothing has invalidated is at generation %d, want 0", gen)
	}

	agg := cache.Aggregate{Etag: "e", RSize: 1, RCount: 1}
	for _, dir := range []string{"a", "b", "a/x"} {
		if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
			return p.cache.PutDirEtag(ctx, tx, testShare, ids[dir], agg, gen)
		}); err != nil {
			t.Fatalf("storing the aggregate for %s: %v", dir, err)
		}
	}

	var bumped uint64
	if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		var err error
		bumped, err = p.cache.BumpShareGen(ctx, tx, testShare)
		return err
	}); err != nil {
		t.Fatalf("bumping: %v", err)
	}
	if bumped != 1 {
		t.Errorf("the first bump returned generation %d, want 1", bumped)
	}

	for _, dir := range []string{"a", "b", "a/x"} {
		if _, ok, err := p.cache.DirEtag(ctx, testShare, ids[dir]); err != nil || ok {
			t.Errorf("%s survived the bump: found %v, err %v", dir, ok, err)
		}
	}

	// And a fresh aggregate stamped with the new generation is usable again.
	if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		return p.cache.PutDirEtag(ctx, tx, testShare, ids["a"], agg, bumped)
	}); err != nil {
		t.Fatalf("re-storing: %v", err)
	}
	if _, ok, err := p.cache.DirEtag(ctx, testShare, ids["a"]); err != nil || !ok {
		t.Errorf("an aggregate stamped with the current generation was refused: found %v, err %v", ok, err)
	}
}

// The size guard gates growth and nothing else. A new id is growth; catching
// up with a file that already has one is not.
func TestTheSizeGuardRefusesANewIDAndNotARefresh(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, shallowFirst(tree(), false))

	p.cf.SetWritesBlocked(true)
	defer p.cf.SetWritesBlocked(false)

	err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		_, err := p.cache.Upsert(ctx, tx, testShare, cache.RootID, "new",
			entry{path: "new", ino: 900, btime: btime(1)}.stat())
		return err
	})
	if err == nil {
		t.Error("a new id was allocated while writes were blocked")
	}

	if err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		got, err := p.cache.Upsert(ctx, tx, testShare, cache.RootID, "a",
			entry{path: "a", ino: 10, dir: true, btime: btime(1_000)}.stat())
		if err == nil && got != ids["a"] {
			t.Errorf("the refresh returned id %d, want %d", got, ids["a"])
		}
		return err
	}); err != nil {
		t.Errorf("a file that already had an id was refused: %v", err)
	}
}
