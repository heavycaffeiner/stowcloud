//go:build linux

package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// recording is a writable share with a live journal, which is what Recent
// reads.
func recording(t *testing.T) (c *Core, st *state.DB, host string, root Resolved) {
	t.Helper()
	c, st, host, root = writable(t)
	attachJournal(t, c)
	return c, st, host, root
}

// timeOf turns a nanosecond stamp into the time Chtimes wants.
func timeOf(ns int64) time.Time { return time.Unix(0, ns) }

func TestANilJournalAnswersEmpty(t *testing.T) {
	c, _, _, _ := writable(t)

	// A deployment that kept no history is not an error, and an empty list
	// is the honest answer rather than a synthesized fallback.
	hits, err := c.Recent(context.Background(), 1, RecentQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Recent with no journal: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("a nil journal produced %d hits", len(hits))
	}
}

func TestARecordedWriteComesBackNavigable(t *testing.T) {
	c, _, host, root := recording(t)
	ctx := context.Background()

	mustCreate(t, c, at(t, root, "note.txt"), "body")
	_ = host

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Recent returned %d hits, want one", len(hits))
	}
	h := hits[0]
	if h.Name != "note.txt" {
		t.Fatalf("the hit names %q", h.Name)
	}
	if h.Vpath.String() != "Documents/note.txt" {
		t.Fatalf("the hit's vpath is %q", h.Vpath.String())
	}
	if h.Share != "Documents" {
		t.Fatalf("the hit's label is %q", h.Share)
	}
	if h.Op != journal.OpUpload {
		t.Fatalf("the hit's op is %v, want upload", h.Op)
	}
	if h.Size != 4 {
		t.Fatalf("the hit reports %d bytes, want 4 from the current stat", h.Size)
	}
	if h.AtNs == 0 || h.MTimeNs == 0 {
		t.Fatalf("the hit carries no timestamps: %+v", h)
	}
}

func TestAtNsIsTheWriteTimeNotTheModificationTime(t *testing.T) {
	c, _, host, root := recording(t)
	ctx := context.Background()
	mustCreate(t, c, at(t, root, "restored.txt"), "body")

	// A restore or a copy that preserved timestamps has an old mtime and a
	// recent write time, which is the distinction the two fields exist for.
	old := int64(1_000_000_000)
	if err := os.Chtimes(filepath.Join(host, "restored.txt"),
		timeOf(old), timeOf(old)); err != nil {
		t.Fatalf("ageing the file: %v", err)
	}

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Recent returned %d hits, want one", len(hits))
	}
	if hits[0].MTimeNs >= hits[0].AtNs {
		t.Fatalf("the aged file reports mtime %d and write time %d, want the write later",
			hits[0].MTimeNs, hits[0].AtNs)
	}
}

func TestRecentIsNewestFirstAndBoundedByLimit(t *testing.T) {
	c, _, _, root := recording(t)
	ctx := context.Background()
	for _, name := range []string{"first.txt", "second.txt", "third.txt"} {
		mustCreate(t, c, at(t, root, name), "x")
	}

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("Recent returned %d hits, want three", len(hits))
	}
	if hits[0].Name != "third.txt" {
		t.Fatalf("the newest hit is %q, want the last write", hits[0].Name)
	}

	bounded, err := c.Recent(ctx, 1, RecentQuery{Limit: 2})
	if err != nil {
		t.Fatalf("bounded Recent: %v", err)
	}
	if len(bounded) != 2 {
		t.Fatalf("a limit of two returned %d hits", len(bounded))
	}
}

func TestScopeKeepsOnlyRowsUnderOneSubtree(t *testing.T) {
	c, _, host, root := recording(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(host, "inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	mustCreate(t, c, at(t, root, "top.txt"), "x")
	mustCreate(t, c, at(t, root, "inner/leaf.txt"), "y")

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10, Scope: "Documents/inner"})
	if err != nil {
		t.Fatalf("scoped Recent: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "leaf.txt" {
		t.Fatalf("the scoped listing is %+v, want only the inner file", hits)
	}
}

func TestSinceNsWindowsTheResult(t *testing.T) {
	c, _, _, root := recording(t)
	ctx := context.Background()
	mustCreate(t, c, at(t, root, "note.txt"), "x")

	all, err := c.Recent(ctx, 1, RecentQuery{Limit: 10})
	if err != nil || len(all) != 1 {
		t.Fatalf("Recent: %v (%d hits)", err, len(all))
	}
	// A window starting after the only write excludes it.
	windowed, err := c.Recent(ctx, 1, RecentQuery{Limit: 10, SinceNs: all[0].AtNs + 1})
	if err != nil {
		t.Fatalf("windowed Recent: %v", err)
	}
	if len(windowed) != 0 {
		t.Fatalf("a window past the write returned %d hits", len(windowed))
	}
}

func TestARowWhoseFileWentAwayDisappears(t *testing.T) {
	c, _, host, root := recording(t)
	ctx := context.Background()
	mustCreate(t, c, at(t, root, "gone.txt"), "x")
	mustCreate(t, c, at(t, root, "kept.txt"), "y")

	if err := os.Remove(filepath.Join(host, "gone.txt")); err != nil {
		t.Fatalf("removing: %v", err)
	}

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	// The row is revalidated rather than trusted: written once and gone since.
	if len(hits) != 1 || hits[0].Name != "kept.txt" {
		t.Fatalf("the listing is %+v, want only the surviving file", hits)
	}
}

func TestARevokedSubtreeGrantHidesItsRows(t *testing.T) {
	c, st, host, root := recording(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(host, "secret"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	mustCreate(t, c, at(t, root, "open.txt"), "x")
	mustCreate(t, c, at(t, root, "secret/hidden.txt"), "y")

	// The share stays readable and only the subtree is denied, so the
	// per-path re-resolve is what has to catch this. A share-level check
	// would let the row through.
	denyReadAt(t, c, st, 1, 10, "secret", allPerms)

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	for _, h := range hits {
		if h.Name == "hidden.txt" {
			t.Fatal("a row under a revoked subtree survived revalidation")
		}
	}
	if len(hits) != 1 || hits[0].Name != "open.txt" {
		t.Fatalf("the listing is %+v, want only the readable file", hits)
	}
}

func TestAnAccountPastTheJournalWidthErrors(t *testing.T) {
	c, _, _, _ := recording(t)

	// The journal's account column is narrower than a user id, and a value
	// that does not fit is an error rather than a truncation into some other
	// account's history.
	if _, err := c.Recent(context.Background(), 1<<40, RecentQuery{Limit: 10}); err == nil {
		t.Fatal("a user id past the journal's width was accepted")
	}
}
