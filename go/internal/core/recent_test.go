package core

import (
	"context"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Recency, checked through the revalidation that makes it safe to return.
//
// A journal row records that an account wrote a file. It does not record that
// they may still read it, or that it still exists, so both are checked again on
// the way out. Without that, a revoked grant leaves the file listed with its
// path and size, which is the whole thing a revocation is for.

const recentUser = UserID(42)

// recordWrite puts a row in the journal for a path under the test share, which
// is what a write through this server leaves behind.
func recordWrite(t *testing.T, c *Core, s *store.Store, rel string) {
	t.Helper()
	p, err := vfs.ParseSharePath(rel)
	if err != nil {
		t.Fatalf("the test path %q: %v", rel, err)
	}
	if jerr := s.Journal().Record(ctx(), journal.Event{
		Account: uint32(recentUser),
		Share:   1,
		Path:    p,
		Op:      journal.OpUpload,
	}); jerr != nil {
		t.Fatalf("recording a write: %v", jerr)
	}
	_ = c
}

func TestARecentListingReturnsWhatTheAccountWrote(t *testing.T) {
	c, s, _ := testCore(t)
	recordWrite(t, c, s, "a.txt")

	got, err := c.Recent(context.Background(), recentUser, RecentQuery{})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d writes, want the one recorded: %+v", len(got), got)
	}
	h := got[0]
	if h.Name != "a.txt" {
		t.Errorf("the hit is named %q", h.Name)
	}
	if h.Vpath.String() == "" || h.Share == "" {
		t.Errorf("the hit is missing what makes it navigable: %+v", h)
	}
	// The size comes from the file, not from the row: the row records that a
	// write happened, not what the file is now.
	if h.Size != uint64(len("hello")) {
		t.Errorf("size = %d, want the file's own", h.Size)
	}
	if h.Op != journal.OpUpload {
		t.Errorf("the write is recorded as %q", h.Op)
	}
}

// A row for a file that is gone is not listed. The row survives the file, so
// returning it would name a path that answers not-found.
func TestAFileThatIsGoneIsNotListed(t *testing.T) {
	c, s, _ := testCore(t)
	recordWrite(t, c, s, "a.txt")
	recordWrite(t, c, s, "never-existed.txt")

	got, err := c.Recent(context.Background(), recentUser, RecentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range got {
		if h.Name == "never-existed.txt" {
			t.Fatal("a row for a file that is not there was listed, so its path is visible for nothing")
		}
	}
	if len(got) != 1 {
		t.Errorf("got %d writes, want only the one whose file exists", len(got))
	}
}

// A grant revoked after the write hides the row. The account wrote it; that
// does not mean they may still see it.
func TestARevokedGrantHidesTheWrite(t *testing.T) {
	c, s, _ := testCore(t)
	recordWrite(t, c, s, "a.txt")

	if got, err := c.Recent(context.Background(), recentUser, RecentQuery{}); err != nil || len(got) != 1 {
		t.Fatalf("the write was not listed before revocation: %d hits, %v", len(got), err)
	}

	// Everything taken away, which is what a removed grant leaves behind.
	c.acl.ReplaceGrants(nil)

	got, err := c.Recent(context.Background(), recentUser, RecentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a revoked account still sees %d of its own writes", len(got))
	}
}

// A write by somebody else is not this account's. The journal is keyed by
// account, and a listing that ignored that would name another person's paths.
func TestAnotherAccountsWritesAreNotListed(t *testing.T) {
	c, s, _ := testCore(t)

	p, perr := vfs.ParseSharePath("a.txt")
	if perr != nil {
		t.Fatal(perr)
	}
	if jerr := s.Journal().Record(ctx(), journal.Event{
		Account: 999, Share: 1, Path: p, Op: journal.OpEdit,
	}); jerr != nil {
		t.Fatal(jerr)
	}

	got, err := c.Recent(context.Background(), recentUser, RecentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("another account's writes were listed: %+v", got)
	}
}

// The window is an instant, so both ends of the wire mean the same thing by it.
func TestTheWindowBoundsTheListing(t *testing.T) {
	c, s, _ := testCore(t)
	recordWrite(t, c, s, "a.txt")

	// The fixture's clock is fixed, so a cutoff past it excludes everything
	// and one before it excludes nothing. That is exactly the boundary.
	after := c.clk.Nanos() + 1
	got, err := c.Recent(context.Background(), recentUser, RecentQuery{SinceNs: after})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a write before the window was returned: %+v", got)
	}

	before := c.clk.Nanos() - 1
	inside, ierr := c.Recent(context.Background(), recentUser, RecentQuery{SinceNs: before})
	if ierr != nil {
		t.Fatal(ierr)
	}
	if len(inside) != 1 {
		t.Fatalf("got %d writes inside the window, want 1", len(inside))
	}
}

// A scope narrows to one subtree, so a screen showing a folder does not list
// writes from everywhere else.
func TestAScopeNarrowsToOneSubtree(t *testing.T) {
	c, s, _ := testCore(t)
	recordWrite(t, c, s, "a.txt")

	all, err := c.Recent(context.Background(), recentUser, RecentQuery{})
	if err != nil || len(all) != 1 {
		t.Fatalf("the write was not listed: %d, %v", len(all), err)
	}

	// The share's own subtree contains it; a sibling name does not.
	vp := all[0].Vpath.String()
	dir := vp[:strings.LastIndexByte(vp, '/')]

	if got, gerr := c.Recent(context.Background(), recentUser, RecentQuery{Scope: dir}); gerr != nil || len(got) != 1 {
		t.Fatalf("the write inside the scope was dropped: %d, %v", len(got), gerr)
	}
	if got, gerr := c.Recent(context.Background(), recentUser, RecentQuery{Scope: "/nowhere"}); gerr != nil || len(got) != 0 {
		t.Fatalf("a write outside the scope was returned: %d, %v", len(got), gerr)
	}
}

// A Core with no journal answers empty rather than failing: a deployment that
// kept no history is a configuration, not an error.
func TestNoJournalIsAnEmptyListing(t *testing.T) {
	c, _, _ := testCore(t)
	c.journal = nil

	got, err := c.Recent(context.Background(), recentUser, RecentQuery{})
	if err != nil {
		t.Fatalf("a deployment with no journal reported an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d hits with no journal", len(got))
	}
}

var _ = acl.Read
