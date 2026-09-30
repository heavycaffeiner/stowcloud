//go:build linux

package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
)

// recording is a writable share with a live journal, which is what Recent
// reads.
func recording(t *testing.T) (c *Core, st *state.DB, host string, root Resolved) {
	t.Helper()
	c, st, host, root = writable(t)
	attachJournal(t, c)
	return c, st, host, root
}

func TestANilJournalAnswersEmpty(t *testing.T) {
	t.Parallel()
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

func TestScopeKeepsOnlyRowsUnderOneSubtree(t *testing.T) {
	t.Parallel()
	c, _, host, root := recording(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(host, "inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(host, "inner2"), 0o755); err != nil {
		t.Fatalf("building the sibling tree: %v", err)
	}
	mustCreate(t, c, at(t, root, "top.txt"), "x")
	mustCreate(t, c, at(t, root, "inner/leaf.txt"), "y")
	mustCreate(t, c, at(t, root, "inner2/not-in-scope.txt"), "z")

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10, Scope: "Documents/inner"})
	if err != nil {
		t.Fatalf("scoped Recent: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "leaf.txt" {
		t.Fatalf("the scoped listing is %+v, want only the inner file", hits)
	}
}

func TestARowWhoseFileWentAwayDisappears(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	c, _, _, _ := recording(t)

	// The journal's account column is narrower than a user id, and a value
	// that does not fit is an error rather than a truncation into some other
	// account's history.
	if _, err := c.Recent(context.Background(), 1<<40, RecentQuery{Limit: 10}); err == nil {
		t.Fatal("a user id past the journal's width was accepted")
	}
}

func TestARowWhoseShareWentAwayDisappears(t *testing.T) {
	t.Parallel()
	c, _, _, root := recording(t)
	ctx := context.Background()
	mustCreate(t, c, at(t, root, "note.txt"), "x")

	// The share stops being visible to the account entirely, which the
	// VpathFor step catches before any path-level check runs.
	c.UnregisterShare(10)

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("a row from an unreachable share survived: %+v", hits)
	}
}

func TestDroppedRowsAreNotBackfilledPastTheLimit(t *testing.T) {
	t.Parallel()
	c, _, host, root := recording(t)
	ctx := context.Background()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		mustCreate(t, c, at(t, root, name), "x")
	}
	// The two newest rows fail revalidation. The limit bounds journal work
	// rather than the answer size, so the older surviving rows are not
	// pulled in to make the count up; a second page is the client's request
	// to make.
	for _, name := range []string{"c.txt", "d.txt"} {
		if err := os.Remove(filepath.Join(host, name)); err != nil {
			t.Fatalf("removing %s: %v", name, err)
		}
	}

	hits, err := c.Recent(ctx, 1, RecentQuery{Limit: 3})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	// Three rows read, two dropped, one returned.
	if len(hits) != 1 || hits[0].Name != "b.txt" {
		t.Fatalf("the listing is %+v, want the single surviving row of the three read", hits)
	}
}
