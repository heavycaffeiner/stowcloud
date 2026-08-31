//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// listable is one share the caller may read and download, resolved at its
// root, which is where every listing test starts.
func listable(t *testing.T) (c *Core, st *state.DB, host string, r Resolved) {
	t.Helper()
	c, st = newCore(t)
	seedUser(t, st, 1, "ada")
	_, host = share(t, c, 10, "documents")
	grantAt(t, c, st, 1, 10, "", "Documents", acl.Read|acl.Download)

	r, err := c.Resolve(1, vpath(t, "Documents"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the share root: %v", err)
	}
	return c, st, host, r
}

// denyReadAt persists a deeper grant that takes the read bit away over one
// subpath while leaving allow in place, which is the drop-style shape the
// read side has to keep honest.
func denyReadAt(t *testing.T, c *Core, st *state.DB, user int64, share ShareID, subpath string, allow acl.Perms) {
	t.Helper()
	ctx := context.Background()
	holder := user
	if _, err := st.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(share),
		Subpath: subpath,
		Allow:   uint16(allow),
		Deny:    uint16(acl.Read),
		Inherit: true,
		Label:   "Documents",
	}, 0); err != nil {
		t.Fatalf("persisting the deny grant: %v", err)
	}
	if err := c.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
}

func writeFile(t *testing.T, host, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(host, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %q: %v", name, err)
	}
}

func names(p Page) []string {
	out := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		out = append(out, e.Name)
	}
	return out
}

func mustList(t *testing.T, c *Core, r Resolved, cur Cursor, opt ListOptions) Page {
	t.Helper()
	p, err := c.ListSorted(context.Background(), r, cur, opt)
	if err != nil {
		t.Fatalf("ListSorted: %v", err)
	}
	return p
}

// TestSortingKeepsEveryNameWithItsOwnKind is the regression for the sort that
// permuted names alone: a directory whose read order is not already sorted
// drew folders as files and files as folders.
func TestSortingKeepsEveryNameWithItsOwnKind(t *testing.T) {
	c, _, host, r := listable(t)
	// Names interleaved so no read order can be the sorted one by accident.
	for _, name := range []string{"zeta", "beta"} {
		if err := os.Mkdir(filepath.Join(host, name), 0o755); err != nil {
			t.Fatalf("creating %q: %v", name, err)
		}
	}
	for _, name := range []string{"alpha.txt", "yankee.txt"} {
		writeFile(t, host, name, "x")
	}

	kinds := map[string]vfs.Kind{
		"beta": vfs.KindDir, "zeta": vfs.KindDir,
		"alpha.txt": vfs.KindFile, "yankee.txt": vfs.KindFile,
	}
	for _, opt := range []ListOptions{{}, {Desc: true}, {Sort: SortKind}, {Sort: SortSize}} {
		p := mustList(t, c, r, "", opt)
		for _, e := range p.Entries {
			if e.Kind != kinds[e.Name] {
				t.Fatalf("under %+v the entry %q is kind %v, want %v", opt, e.Name, e.Kind, kinds[e.Name])
			}
			if e.IsDir != (kinds[e.Name] == vfs.KindDir) {
				t.Fatalf("under %+v the entry %q reports IsDir %v", opt, e.Name, e.IsDir)
			}
		}
	}
}

func TestDirectoriesLeadInBothDirections(t *testing.T) {
	c, _, host, r := listable(t)
	for _, name := range []string{"a-dir", "z-dir"} {
		if err := os.Mkdir(filepath.Join(host, name), 0o755); err != nil {
			t.Fatalf("creating %q: %v", name, err)
		}
	}
	writeFile(t, host, "a-file", "x")
	writeFile(t, host, "z-file", "x")

	asc := mustList(t, c, r, "", ListOptions{})
	if got := names(asc); !equalNames(got, []string{"a-dir", "z-dir", "a-file", "z-file"}) {
		t.Fatalf("ascending order is %v", got)
	}
	desc := mustList(t, c, r, "", ListOptions{Desc: true})
	if got := names(desc); !equalNames(got, []string{"z-dir", "a-dir", "z-file", "a-file"}) {
		t.Fatalf("descending order is %v, want the groups kept and only their contents flipped", got)
	}
	if asc.Dirs != 2 || desc.Dirs != 2 {
		t.Fatalf("the pages count %d and %d directories, want 2", asc.Dirs, desc.Dirs)
	}
}

func TestSortBySizeAndMtimeOrderByTheStatValue(t *testing.T) {
	c, _, host, r := listable(t)
	writeFile(t, host, "small.txt", "a")
	writeFile(t, host, "large.txt", "aaaaaaaaaa")
	// Two files of one size, so the name tiebreak is exercised.
	writeFile(t, host, "mid-b.txt", "aaaaa")
	writeFile(t, host, "mid-a.txt", "aaaaa")

	asc := names(mustList(t, c, r, "", ListOptions{Sort: SortSize}))
	if !equalNames(asc, []string{"small.txt", "mid-a.txt", "mid-b.txt", "large.txt"}) {
		t.Fatalf("ascending by size is %v", asc)
	}
	desc := names(mustList(t, c, r, "", ListOptions{Sort: SortSize, Desc: true}))
	// The name tiebreak stays ascending under Desc, so the two mid files keep
	// their relative order while the size groups reverse.
	if !equalNames(desc, []string{"large.txt", "mid-a.txt", "mid-b.txt", "small.txt"}) {
		t.Fatalf("descending by size is %v", desc)
	}

	base := time.Unix(1_700_000_000, 0)
	for i, name := range []string{"large.txt", "mid-a.txt", "mid-b.txt", "small.txt"} {
		when := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(filepath.Join(host, name), when, when); err != nil {
			t.Fatalf("stamping %q: %v", name, err)
		}
	}
	byTime := names(mustList(t, c, r, "", ListOptions{Sort: SortMtime}))
	if !equalNames(byTime, []string{"large.txt", "mid-a.txt", "mid-b.txt", "small.txt"}) {
		t.Fatalf("ascending by mtime is %v", byTime)
	}
}

// TestASymlinkIsTypedFromTheDirectoryRead covers the kind fallback: the stat
// cannot type a symlink under the deny policy, so the entry would carry
// KindOther without the directory read's answer.
func TestASymlinkIsTypedFromTheDirectoryRead(t *testing.T) {
	c, _, host, r := listable(t)
	writeFile(t, host, "target.txt", "x")
	if err := os.Symlink("target.txt", filepath.Join(host, "link.txt")); err != nil {
		t.Fatalf("creating the symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(host, "dir"), 0o755); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}

	p := mustList(t, c, r, "", ListOptions{})
	var link Entry
	for _, e := range p.Entries {
		if e.Name == "link.txt" {
			link = e
		}
	}
	if link.Kind != vfs.KindSymlink {
		t.Fatalf("the symlink is typed %v, want KindSymlink", link.Kind)
	}
	if link.IsDir {
		t.Fatal("the symlink reports itself a directory")
	}
	// It sorts with the files: the group test reads the directory entry's
	// kind, and a symlink is not a directory whatever it points at.
	if got := names(p); !equalNames(got, []string{"dir", "link.txt", "target.txt"}) {
		t.Fatalf("the listing order is %v, want the symlink among the files", got)
	}
	if p.Dirs != 1 {
		t.Fatalf("the page counts %d directories, want 1", p.Dirs)
	}
}

// TestAVanishedEntryIsASkeletonRow uses a dangling symlink, whose stat fails
// exactly as a deleted entry's does, so the fallback is exercised without a
// race the test cannot win.
func TestAVanishedEntryIsASkeletonRow(t *testing.T) {
	c, _, host, r := listable(t)
	writeFile(t, host, "present.txt", "hello")
	if err := os.Symlink("nothing-here", filepath.Join(host, "gone.txt")); err != nil {
		t.Fatalf("creating the dangling symlink: %v", err)
	}

	p := mustList(t, c, r, "", ListOptions{})
	if len(p.Entries) != 2 {
		t.Fatalf("the listing returned %d rows, want both names", len(p.Entries))
	}
	for _, e := range p.Entries {
		if e.Name != "gone.txt" {
			continue
		}
		if e.Size != 0 || e.ETag != "" || e.MTimeNs != 0 {
			t.Fatalf("the skeleton row carries content: %+v", e)
		}
		if e.Perms != r.Perms() {
			t.Fatalf("the skeleton row carries perms %v, want the resolution's", e.Perms)
		}
		if e.Path.String() != "gone.txt" {
			t.Fatalf("the skeleton row is at %q", e.Path)
		}
	}
}

func TestPagingWalksTheDirectoryOnceWithStableAccounting(t *testing.T) {
	c, _, host, r := listable(t)
	const total = 25
	for i := range total {
		writeFile(t, host, "f"+strconv.Itoa(1000+i)+".txt", "x")
	}

	seen := map[string]bool{}
	cur := Cursor("")
	pages := 0
	for {
		p := mustList(t, c, r, cur, ListOptions{Limit: 10})
		if p.Total != total || p.Dirs != 0 {
			t.Fatalf("page %d reports total %d dirs %d, want %d and 0", pages, p.Total, p.Dirs, total)
		}
		for _, e := range p.Entries {
			if seen[e.Name] {
				t.Fatalf("%q appeared on two pages", e.Name)
			}
			seen[e.Name] = true
		}
		pages++
		if p.Next == "" {
			break
		}
		cur = p.Next
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
	}
	if len(seen) != total {
		t.Fatalf("paging saw %d of %d entries", len(seen), total)
	}
	if pages != 3 {
		t.Fatalf("paging took %d pages of 10 over %d entries", pages, total)
	}
}

func TestCursorRefusals(t *testing.T) {
	c, _, host, r := listable(t)
	writeFile(t, host, "a.txt", "x")
	writeFile(t, host, "b.txt", "x")

	for _, cur := range []Cursor{"nonsense", "-1", "3"} {
		if _, err := c.ListSorted(context.Background(), r, cur, ListOptions{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("listing at cursor %q = %v, want ErrNotFound", cur, err)
		}
	}

	// Exactly at the end is not a refusal: a client that paged to the
	// boundary gets an empty page with the directory's own accounting.
	p := mustList(t, c, r, "2", ListOptions{})
	if len(p.Entries) != 0 || p.Next != "" {
		t.Fatalf("the boundary page is %+v, want empty with no next", p)
	}
	if p.Total != 2 || p.DirEtag == "" {
		t.Fatalf("the boundary page reports total %d etag %q", p.Total, p.DirEtag)
	}
}

func TestTheLimitDefaultsAndIsClamped(t *testing.T) {
	c, _, host, r := listable(t)
	// 201 entries: one more than the default page, so the default is
	// observable without minting two thousand files.
	for i := range pageSize + 1 {
		writeFile(t, host, "f"+strconv.Itoa(10000+i)+".txt", "x")
	}

	if p := mustList(t, c, r, "", ListOptions{}); len(p.Entries) != pageSize {
		t.Fatalf("the default page holds %d rows, want %d", len(p.Entries), pageSize)
	}
	// A limit above the ceiling is clamped rather than refused, and here the
	// clamp is above the directory, so the whole of it comes back.
	p := mustList(t, c, r, "", ListOptions{Limit: maxPageSize + 500})
	if len(p.Entries) != pageSize+1 || p.Next != "" {
		t.Fatalf("an over-ceiling limit returned %d rows with next %q", len(p.Entries), p.Next)
	}
}

func TestListingAFileIsNotFoundAndListingWithoutReadIsDenied(t *testing.T) {
	c, st, host, _ := listable(t)
	writeFile(t, host, "readme.txt", "x")

	file, err := c.Resolve(1, vpath(t, "Documents/readme.txt"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the file: %v", err)
	}
	if _, lerr := c.ListSorted(context.Background(), file, "", ListOptions{}); !errors.Is(lerr, ErrNotFound) {
		t.Fatalf("listing a file = %v, want ErrNotFound", lerr)
	}

	// A drop-style subtree: the caller may put bytes through it but may not
	// see what is in it, so a resolution earned with Download alone carries
	// no Read bit and Require refuses before the directory is touched.
	if merr := os.Mkdir(filepath.Join(host, "drop"), 0o755); merr != nil {
		t.Fatalf("creating the drop directory: %v", merr)
	}
	denyReadAt(t, c, st, 1, 10, "drop", acl.Download)
	drop, derr := c.Resolve(1, vpath(t, "Documents/drop"), acl.Download)
	if derr != nil {
		t.Fatalf("resolving the drop directory: %v", derr)
	}
	if _, lerr := c.ListSorted(context.Background(), drop, "", ListOptions{}); !errors.Is(lerr, ErrDenied) {
		t.Fatalf("listing without Read = %v, want ErrDenied", lerr)
	}

	// A missing directory answers the same as everything else missing.
	missing, rerr := c.Resolve(1, vpath(t, "Documents/nothing"), acl.Read)
	if rerr != nil {
		t.Fatalf("resolving a missing directory: %v", rerr)
	}
	if _, lerr := c.ListSorted(context.Background(), missing, "", ListOptions{}); !errors.Is(lerr, ErrNotFound) {
		t.Fatalf("listing a missing directory = %v, want ErrNotFound", lerr)
	}
}

func TestReservedControlNamesNeverReachAPage(t *testing.T) {
	c, _, host, r := listable(t)
	writeFile(t, host, "visible.txt", "x")
	for _, name := range []string{".scpart-abcd", ".scmeta", ".sctrash", ".scindex"} {
		writeFile(t, host, name, "x")
	}

	p := mustList(t, c, r, "", ListOptions{})
	if got := names(p); !equalNames(got, []string{"visible.txt"}) {
		t.Fatalf("the listing shows %v, want only the ordinary name", got)
	}
	if p.Total != 1 {
		t.Fatalf("the page counts %d entries, want the control names excluded from the total", p.Total)
	}
}

func TestThePageCarriesTheDirectoryInodesOwnToken(t *testing.T) {
	c, _, host, r := listable(t)
	writeFile(t, host, "a.txt", "x")

	st, err := r.Root().Stat(r.Path())
	if err != nil {
		t.Fatalf("stating the directory: %v", err)
	}
	wantETag, wantWeak := FileETag(st)
	p := mustList(t, c, r, "", ListOptions{})
	if p.DirEtag != wantETag || p.DirEtagWeak != wantWeak {
		t.Fatalf("the page's token is %q/%v, want %q/%v", p.DirEtag, p.DirEtagWeak, wantETag, wantWeak)
	}
}

func TestCursorOffsetParsing(t *testing.T) {
	cases := []struct {
		in      Cursor
		want    int
		refused bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"42", 42, false},
		{"-1", 0, true},
		{"x", 0, true},
		{"1.5", 0, true},
	}
	for _, tc := range cases {
		got, err := cursorOffset(tc.in)
		if tc.refused {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("cursorOffset(%q) = %v, want ErrNotFound", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("cursorOffset(%q) = %d, %v, want %d", tc.in, got, err, tc.want)
		}
	}
}

func equalNames(got, want []string) bool { return slices.Equal(got, want) }
