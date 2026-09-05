//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

func rollup(t *testing.T, c *Core, share ShareID, p string) Aggregate {
	t.Helper()
	agg, err := c.Aggregate(context.Background(), share, safe(t, p))
	if err != nil {
		t.Fatalf("Aggregate(%q): %v", p, err)
	}
	return agg
}

// tree builds a small fixture whose totals are known by hand.
func tree(t *testing.T, hostDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(hostDir, "docs/2024"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, hostDir, "docs/note.txt", "aaa")
	writeFile(t, hostDir, "docs/2024/report.txt", "bbbb")
	writeFile(t, hostDir, "top.txt", "cc")
}

func TestARollupCountsTheWholeSubtree(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	tree(t, hostDir)

	root := rollup(t, c, 10, "")
	if root.RSize != 9 || root.RCount != 3 {
		t.Fatalf("the root rollup is %+v, want 9 bytes over 3 files", root)
	}
	docs := rollup(t, c, 10, "docs")
	if docs.RSize != 7 || docs.RCount != 2 {
		t.Fatalf("the docs rollup is %+v, want 7 bytes over 2 files", docs)
	}
	if root.Etag == "" || docs.Etag == root.Etag {
		t.Fatalf("the two rollups carry etags %q and %q", root.Etag, docs.Etag)
	}
}

func TestARollupIsStableAndChangesWithTheTree(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	tree(t, hostDir)
	ctx := context.Background()

	first := rollup(t, c, 10, "")
	if again := rollup(t, c, 10, ""); again != first {
		t.Fatalf("a second rollup of an unchanged tree gave %+v, want %+v", again, first)
	}

	// Every shape of change moves the token: content, a new name, a
	// removal, a rename.
	for _, change := range []struct {
		name string
		do   func()
	}{
		{"content", func() { writeFile(t, hostDir, "docs/note.txt", "zzz") }},
		{"a new file", func() { writeFile(t, hostDir, "docs/extra.txt", "e") }},
		{"a name change", func() {
			// The same bytes under a different name: the name is folded
			// into the hash, so this moves the token on its own.
			writeFile(t, hostDir, "docs/renamed.txt", "e")
			if err := os.Remove(filepath.Join(hostDir, "docs/extra.txt")); err != nil {
				t.Fatalf("removing the old name: %v", err)
			}
		}},
		{"a removal", func() {
			if err := os.Remove(filepath.Join(hostDir, "docs/renamed.txt")); err != nil {
				t.Fatalf("removing: %v", err)
			}
		}},
	} {
		before := rollup(t, c, 10, "")
		change.do()
		// The cache is keyed on generation and dirty flags, neither of
		// which a change made behind the core's back moves, so the test
		// invalidates explicitly.
		if err := c.InvalidateShare(ctx, 10); err != nil {
			t.Fatalf("invalidating: %v", err)
		}
		if after := rollup(t, c, 10, ""); after.Etag == before.Etag {
			t.Fatalf("%s left the etag at %q", change.name, after.Etag)
		}
	}
}

// TestTheRollupDoesNotDependOnCreationOrder builds two shares holding the
// same names, created in opposite orders. The children are directories,
// whose contribution is their own rollup rather than an inode-derived file
// token, so anything left of a match is the order the names were folded in.
func TestTheRollupDoesNotDependOnCreationOrder(t *testing.T) {
	c, _, hostA, _ := writable(t)
	forward := []string{"alpha", "bravo", "charlie", "delta"}
	for _, name := range forward {
		if err := os.Mkdir(filepath.Join(hostA, name), 0o755); err != nil {
			t.Fatalf("creating %q: %v", name, err)
		}
	}

	_, hostB := share(t, c, 11, "second")
	for i := len(forward) - 1; i >= 0; i-- {
		if err := os.Mkdir(filepath.Join(hostB, forward[i]), 0o755); err != nil {
			t.Fatalf("creating %q: %v", forward[i], err)
		}
	}

	a := rollup(t, c, 10, "")
	b := rollup(t, c, 11, "")
	if a.Etag != b.Etag {
		t.Fatalf("two identically named trees hashed to %q and %q", a.Etag, b.Etag)
	}
	if a.RSize != b.RSize || a.RCount != b.RCount {
		t.Fatalf("the totals differ: %+v against %+v", a, b)
	}
}

func TestAggregateRefusesWhatItCannotRollUp(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	writeFile(t, hostDir, "plain.txt", "x")

	if _, err := c.Aggregate(context.Background(), 10, safe(t, "plain.txt")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolling up a file = %v, want ErrNotFound", err)
	}
	if _, err := c.Aggregate(context.Background(), 99, vfs.RootPath()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolling up an unregistered share = %v, want ErrNotFound", err)
	}
}

// TestADanglingChildIsSkippedByTheRollup uses a dangling symlink, whose stat
// fails exactly as a child deleted mid-walk does.
func TestADanglingChildIsSkippedByTheRollup(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	writeFile(t, hostDir, "real.txt", "abc")
	if err := os.Symlink("nowhere", filepath.Join(hostDir, "ghost.txt")); err != nil {
		t.Fatalf("creating the dangling symlink: %v", err)
	}

	agg := rollup(t, c, 10, "")
	if agg.RCount != 1 || agg.RSize != 3 {
		t.Fatalf("the rollup is %+v, want only the real file counted", agg)
	}
}

// TestReEnteringAHeldFileIDSkipsItsGuard is the rule that bounds a hard-link
// cycle: a directory and an alias of it share one file id, and the second
// visit must not block on the guard the first visit is holding. The guard is
// locked here deliberately, so a call that tried to take it again would
// never return.
func TestReEnteringAHeldFileIDSkipsItsGuard(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	writeFile(t, hostDir, "one.txt", "abc")

	w := aggWalk{
		root:   mustRoot(t, c, 10),
		share:  10,
		guards: map[ident.FileID]*sync.Mutex{},
	}
	w.guard(ident.RootID).Lock()
	defer w.guard(ident.RootID).Unlock()

	agg, err := c.computeAggregate(context.Background(), w,
		vfs.RootPath(), ident.RootID, []ident.FileID{ident.RootID})
	if err != nil {
		t.Fatalf("re-entering a held id: %v", err)
	}
	if agg.RCount != 1 || agg.RSize != 3 {
		t.Fatalf("the re-entrant rollup is %+v", agg)
	}
}

func TestTheSecondRollupComesFromTheCache(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	tree(t, hostDir)
	ctx := context.Background()

	first := rollup(t, c, 10, "")

	// Change the tree behind the core's back. A cached read is the only
	// way the second call can still answer the old token.
	writeFile(t, hostDir, "docs/note.txt", "changed-content-entirely")
	if second := rollup(t, c, 10, ""); second != first {
		t.Fatalf("the second rollup recomputed: %+v against %+v", second, first)
	}

	// A generation bump makes every cached row stale in one step, without
	// walking or naming a path.
	if err := c.InvalidateShare(ctx, 10); err != nil {
		t.Fatalf("invalidating: %v", err)
	}
	if third := rollup(t, c, 10, ""); third == first {
		t.Fatalf("the rollup after a bump is still %+v", third)
	}
}

func TestMarkDirtyInvalidatesTheAncestorChainOnly(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(hostDir, "left"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "right"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, hostDir, "left/a.txt", "a")
	writeFile(t, hostDir, "right/b.txt", "b")

	leftBefore := rollup(t, c, 10, "left")
	rightBefore := rollup(t, c, 10, "right")
	rootBefore := rollup(t, c, 10, "")

	// Change both subtrees behind the core's back, then mark only the left
	// one. The right one's cached row must survive.
	writeFile(t, hostDir, "left/a.txt", "aaaaaa")
	writeFile(t, hostDir, "right/b.txt", "bbbbbb")
	c.markDirty(ctx, 10, safe(t, "left/a.txt"))

	if got := rollup(t, c, 10, "left"); got == leftBefore {
		t.Fatalf("the marked subtree still answers %+v", got)
	}
	if got := rollup(t, c, 10, "right"); got != rightBefore {
		t.Fatalf("an unrelated subtree recomputed: %+v against %+v", got, rightBefore)
	}
	if got := rollup(t, c, 10, ""); got == rootBefore {
		t.Fatal("the root aggregate survived a write under it")
	}
}

// TestMarkDirtyOnAVanishedShareIsSilent covers the racing admin action: the
// share is gone, so there is nothing left to invalidate and nothing to fail.
func TestMarkDirtyOnAVanishedShareIsSilent(t *testing.T) {
	c, _, _, _ := writable(t)
	c.markDirty(context.Background(), 99, safe(t, "anything.txt"))
}

// TestARootRollupIsCachedUnderTheSentinel asserts the share root has no node
// row of its own and is still cached: its id is the sentinel, which is what
// makes markDirty able to push it unconditionally.
func TestARootRollupIsCachedUnderTheSentinel(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	tree(t, hostDir)
	ctx := context.Background()

	id, err := c.ensureFileIDChain(ctx, mustRoot(t, c, 10), 10, vfs.RootPath())
	if err != nil {
		t.Fatalf("ensuring the chain for the root: %v", err)
	}
	if id != ident.RootID {
		t.Fatalf("the share root's id is %d, want the sentinel", id)
	}

	first := rollup(t, c, 10, "")
	agg, ok, err := c.cache.DirEtag(ctx, 10, ident.RootID)
	if err != nil {
		t.Fatalf("reading the cached root aggregate: %v", err)
	}
	if !ok || agg != first {
		t.Fatalf("the cached root aggregate is %+v, %v, want %+v", agg, ok, first)
	}
}

func mustRoot(t *testing.T, c *Core, share ShareID) vfs.Root {
	t.Helper()
	root, ok := c.ShareRoot(share)
	if !ok {
		t.Fatalf("share %d is not registered", share)
	}
	return root
}

// TestAMutationInvalidatesWhatItTouched is the write path's own invalidation
// asserted end to end, which is what markDirty exists for.
func TestAMutationInvalidatesWhatItTouched(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	if err := os.MkdirAll(filepath.Join(hostDir, "docs"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, hostDir, "docs/note.txt", "aaa")

	before := rollup(t, c, 10, "docs")
	mustCreate(t, c, under(t, c, "Documents/docs/new.txt", acl.Write), "brand new bytes")
	after := rollup(t, c, 10, "docs")

	if after.Etag == before.Etag {
		t.Fatalf("the write left the aggregate at %q", after.Etag)
	}
	if after.RCount != 2 {
		t.Fatalf("the recomputed rollup is %+v, want two files", after)
	}
}
