//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// folderLink is a share holding a small tree, with a link over one folder in
// it and a sibling folder the link must never reach.
func folderLink(t *testing.T, perms acl.Perms) (c *Core, host string, link Link) {
	t.Helper()
	c, _, host, root, _ := linkable(t)
	if err := os.MkdirAll(filepath.Join(host, "shared/inner"), 0o755); err != nil {
		t.Fatalf("building the shared tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(host, "private"), 0o755); err != nil {
		t.Fatalf("building the private tree: %v", err)
	}
	writeFile(t, host, "shared/b.txt", "bb")
	writeFile(t, host, "shared/a.txt", "a")
	writeFile(t, host, "shared/inner/leaf.txt", "leaf")
	writeFile(t, host, "private/secret.txt", "not for the link")

	return c, host, mustLink(t, c, at(t, root, "shared"), perms)
}

func TestLinkBrowseListsDirectoriesFirstWithRealSizes(t *testing.T) {
	c, _, link := folderLink(t, acl.Read|acl.Download)

	got, err := c.LinkBrowse(context.Background(), link, "")
	if err != nil {
		t.Fatalf("LinkBrowse: %v", err)
	}
	if !got.IsDir || got.Name != "shared" {
		t.Fatalf("the top listing is %+v, want the shared folder", got)
	}
	names := make([]string, 0, len(got.Entries))
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	// Directories first, then by name.
	if strings.Join(names, ",") != "inner,a.txt,b.txt" {
		t.Fatalf("the listing order is %v, want directories first then by name", names)
	}
	for _, e := range got.Entries {
		if e.Name == "b.txt" && e.Size != 2 {
			t.Fatalf("b.txt lists %d bytes, want 2", e.Size)
		}
		if e.Name == "inner" && !e.IsDir {
			t.Fatal("the subdirectory did not list as one")
		}
	}
}

func TestLinkBrowseRefusesEveryPathOutsideTheLinkedFolder(t *testing.T) {
	c, _, link := folderLink(t, acl.Read|acl.Download)
	ctx := context.Background()

	// Parsed rather than joined as text, so none of these can name anything
	// outside the folder the link was made for.
	for _, sub := range []string{"..", "../private", "/etc", "../../etc/passwd", "inner/../../private"} {
		if _, err := c.LinkBrowse(ctx, link, sub); !errors.Is(err, ErrNotFound) {
			t.Fatalf("browsing %q returned %v, want ErrNotFound", sub, err)
		}
	}
	// A legitimate subpath still works.
	if _, err := c.LinkBrowse(ctx, link, "inner"); err != nil {
		t.Fatalf("browsing a real subpath: %v", err)
	}
}

func TestAFileLinkBrowsesAsItselfWithNoEntries(t *testing.T) {
	c, _, host, root, _ := linkable(t)
	writeFile(t, host, "note.txt", "body")
	link := mustLink(t, c, at(t, root, "note.txt"), acl.Read|acl.Download)

	got, err := c.LinkBrowse(context.Background(), link, "")
	if err != nil {
		t.Fatalf("LinkBrowse on a file link: %v", err)
	}
	// One endpoint serves both shapes, which is why a file answers rather
	// than refusing.
	if got.IsDir || len(got.Entries) != 0 {
		t.Fatalf("a file link listed as %+v, want IsDir false with no entries", got)
	}
	if got.Name != "note.txt" || got.Size != 4 {
		t.Fatalf("the file link describes %+v", got)
	}
}

func TestBrowseSeparatesADeadBaseFromAMissingSubpath(t *testing.T) {
	c, host, link := folderLink(t, acl.Read|acl.Download)
	ctx := context.Background()

	// A missing entry inside a live link is an ordinary miss.
	if _, err := c.LinkBrowse(ctx, link, "absent.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing subpath returned %v, want ErrNotFound", err)
	}
	// The base going away is the link dying.
	if err := os.Rename(filepath.Join(host, "shared"), filepath.Join(host, "moved")); err != nil {
		t.Fatalf("renaming the base: %v", err)
	}
	if _, err := c.LinkBrowse(ctx, link, ""); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("a renamed base returned %v, want ErrLinkExpired", err)
	}
}

func TestLinkStreamAtReadsBeneathAFolderLink(t *testing.T) {
	c, _, link := folderLink(t, acl.Read|acl.Download)
	ctx := context.Background()

	_, stream, err := c.LinkStreamAt(ctx, link, "inner/leaf.txt", nil)
	if err != nil {
		t.Fatalf("LinkStreamAt: %v", err)
	}
	if got := drain(t, stream); got != "leaf" {
		t.Fatalf("the stream carried %q, want %q", got, "leaf")
	}
	closeStream(t, stream)

	// A directory is not a stream, and a missing entry is an ordinary miss.
	if _, _, err := c.LinkStreamAt(ctx, link, "inner", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("streaming a directory returned %v, want ErrNotFound", err)
	}
	if _, _, err := c.LinkStreamAt(ctx, link, "absent.txt", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("streaming a missing entry returned %v, want ErrNotFound", err)
	}
	if _, _, err := c.LinkStreamAt(ctx, link, "../private/secret.txt", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("streaming out of the folder returned %v, want ErrNotFound", err)
	}
}

func TestLinkArchiveWalkReadsEveryFileWithNoUser(t *testing.T) {
	c, host, link := folderLink(t, acl.Read|acl.Download)
	ctx := context.Background()

	// The session walk asks the ACL what the resolved user may read, and a
	// link has no user: driven by the ACL it visited every directory and read
	// nothing, producing an empty archive.
	// The walk owns the stream and closes it before the next entry, so the
	// visitor only reads.
	seen := map[string]bool{}
	files := 0
	err := c.LinkArchiveWalk(ctx, link, "", func(e WalkEntry, s *Stream) error {
		seen[e.RelPath] = true
		if s != nil {
			files++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("LinkArchiveWalk: %v", err)
	}
	for _, want := range []string{"a.txt", "b.txt", "inner", "inner/leaf.txt"} {
		if !seen[want] {
			t.Fatalf("the walk missed %q; saw %v", want, seen)
		}
	}
	if files != 3 {
		t.Fatalf("the walk opened %d files, want 3", files)
	}
	if seen["../private/secret.txt"] || seen["secret.txt"] {
		t.Fatal("the walk escaped the linked folder")
	}
	_ = host
}

func TestAnArchiveWalkSurvivesASubtreeThatVanishes(t *testing.T) {
	c, host, link := folderLink(t, acl.Read|acl.Download)
	ctx := context.Background()
	// Unreadable, so the descent into it fails the way a vanished directory
	// does. It must contribute nothing rather than failing the archive.
	if err := os.Chmod(filepath.Join(host, "shared/inner"), 0o000); err != nil {
		t.Fatalf("sealing the subtree: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(host, "shared/inner"), 0o755); err != nil {
			t.Errorf("unsealing: %v", err)
		}
	})

	files := 0
	if err := c.LinkArchiveWalk(ctx, link, "", func(_ WalkEntry, s *Stream) error {
		if s != nil {
			files++
		}
		return nil
	}); err != nil {
		t.Fatalf("an unreadable subtree failed the archive: %v", err)
	}
	if files != 2 {
		t.Fatalf("the partial archive holds %d files, want the two readable ones", files)
	}
}

func TestLinkResolvedCarriesTheLinksOwnPermissions(t *testing.T) {
	c, _, link := folderLink(t, acl.Read|acl.Download)

	r, err := c.LinkResolved(link, "inner")
	if err != nil {
		t.Fatalf("LinkResolved: %v", err)
	}
	if r.Perms() != link.Perms {
		t.Fatalf("the resolution carries %v, want the link's %v", r.Perms(), link.Perms)
	}
	if r.Path().Name() != "inner" {
		t.Fatalf("the resolution landed at %q", r.Path().String())
	}
}

func TestLinkDropRefusesWithoutCreateAndSuffixesATakenName(t *testing.T) {
	ctx := context.Background()

	t.Run("without create", func(t *testing.T) {
		c, _, link := folderLink(t, acl.Read|acl.Download)
		if _, err := c.LinkDrop(ctx, link, "x.txt", []byte("x")); !errors.Is(err, ErrDenied) {
			t.Fatalf("dropping through a read link returned %v, want ErrDenied", err)
		}
	})

	t.Run("a taken name keeps both", func(t *testing.T) {
		c, host, link := folderLink(t, acl.Create)
		if _, err := c.LinkDrop(ctx, link, "a.txt", []byte("dropped")); err != nil {
			t.Fatalf("LinkDrop: %v", err)
		}
		// Never overwrites: the bearer cannot see the folder, so an overwrite
		// would let them destroy a file they cannot name.
		if got := readHost(t, host, "shared/a.txt"); got != "a" {
			t.Fatalf("the existing file became %q", got)
		}
		if got := readHost(t, host, "shared/a (2).txt"); got != "dropped" {
			t.Fatalf("the dropped file landed as %q", got)
		}
	})
}

func TestLinkDropFileRefusesATakenNameAndDoesNotWidenTheLink(t *testing.T) {
	c, host, link := folderLink(t, acl.Create)
	ctx := context.Background()

	entry, err := c.LinkDropFile(ctx, link, "fresh.txt", strings.NewReader("streamed"))
	if err != nil {
		t.Fatalf("LinkDropFile: %v", err)
	}
	if got := readHost(t, host, "shared/fresh.txt"); got != "streamed" {
		t.Fatalf("the streamed file holds %q", got)
	}
	// The Write bit was added for that one resolution only; every other
	// surface still reads the link's own permissions.
	if entry.Perms != link.Perms {
		t.Fatalf("the returned entry carries %v, want the link's %v", entry.Perms, link.Perms)
	}
	if link.Perms.Has(acl.Write) {
		t.Fatal("the link itself was widened with Write")
	}

	if _, err := c.LinkDropFile(ctx, link, "fresh.txt", strings.NewReader("again")); !errors.Is(err, ErrExists) {
		t.Fatalf("a taken name returned %v, want ErrExists", err)
	}
	// A drop bearer still cannot read what it put there.
	if _, _, err := c.LinkStreamAt(ctx, link, "fresh.txt", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("a drop bearer streamed a file, returning %v, want ErrDenied", err)
	}
}

func TestADeadLinkRefusesEveryBearerSurface(t *testing.T) {
	c, host, link := folderLink(t, acl.Read|acl.Download|acl.Create)
	ctx := context.Background()
	// The base is renamed, so the identity cross-check fails everywhere.
	if err := os.Rename(filepath.Join(host, "shared"), filepath.Join(host, "gone")); err != nil {
		t.Fatalf("renaming the base: %v", err)
	}

	if _, err := c.LinkBrowse(ctx, link, ""); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("LinkBrowse returned %v", err)
	}
	if _, _, err := c.LinkStream(ctx, link, nil); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("LinkStream returned %v", err)
	}
	if _, _, err := c.LinkStreamAt(ctx, link, "a.txt", nil); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("LinkStreamAt returned %v", err)
	}
	if _, err := c.LinkDrop(ctx, link, "x.txt", []byte("x")); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("LinkDrop returned %v", err)
	}
}
