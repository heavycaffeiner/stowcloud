// The code under test is Linux only, like the rest of this package.
//go:build linux

package core

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

func namesOf(in []vfs.DirEntry) []string {
	out := make([]string, len(in))
	for i, e := range in {
		out[i] = e.Name
	}
	return out
}

// The sort moves names, kinds and the entries together.
//
// It used to swap names alone, so every kind stayed at the index it started
// in and ended up beside whichever name landed there. A directory read that
// arrived unsorted, which is the normal case, produced folders drawn as files
// and files drawn as folders.
func TestSortingKeepsEachNameWithItsKind(t *testing.T) {
	in := []vfs.DirEntry{
		{Name: "zeta.txt", Kind: vfs.KindFile},
		{Name: "alpha", Kind: vfs.KindDir},
		{Name: "beta.txt", Kind: vfs.KindFile},
		{Name: "gamma", Kind: vfs.KindDir},
	}
	names := namesOf(in)

	sortListing(in, names, ListOptions{}, nil)

	want := []string{"alpha", "gamma", "beta.txt", "zeta.txt"}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("names[%d] = %q, want %q (order: %v)", i, names[i], w, names)
		}
	}
	for i, name := range names {
		if in[i].Name != name {
			t.Fatalf("index %d holds entry %q beside name %q", i, in[i].Name, name)
		}
	}
	kindOf := map[string]vfs.Kind{
		"alpha": vfs.KindDir, "gamma": vfs.KindDir,
		"beta.txt": vfs.KindFile, "zeta.txt": vfs.KindFile,
	}
	for i, name := range names {
		if in[i].Kind != kindOf[name] {
			t.Errorf("%q came out as kind %v, want %v", name, in[i].Kind, kindOf[name])
		}
	}
}

// Descending reverses within each group. Directories stay ahead of files,
// which is what a file manager does and what makes the reverse useful.
func TestDescendingKeepsDirectoriesFirst(t *testing.T) {
	in := []vfs.DirEntry{
		{Name: "adir", Kind: vfs.KindDir},
		{Name: "bfile", Kind: vfs.KindFile},
		{Name: "zdir", Kind: vfs.KindDir},
		{Name: "yfile", Kind: vfs.KindFile},
	}
	names := namesOf(in)

	sortListing(in, names, ListOptions{Desc: true}, nil)

	want := []string{"zdir", "adir", "yfile", "bfile"}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("names[%d] = %q, want %q (order: %v)", i, names[i], w, names)
		}
	}
}

// Ordering by size uses the stat values, and ties fall back to the name so the
// result does not depend on what the kernel happened to return.
func TestSortingBySize(t *testing.T) {
	in := []vfs.DirEntry{
		{Name: "big.bin", Kind: vfs.KindFile},
		{Name: "small.txt", Kind: vfs.KindFile},
		{Name: "same-b.txt", Kind: vfs.KindFile},
		{Name: "same-a.txt", Kind: vfs.KindFile},
	}
	names := namesOf(in)
	stats := map[string]vfs.Stat{
		"big.bin":    {Size: 9000},
		"small.txt":  {Size: 10},
		"same-a.txt": {Size: 500},
		"same-b.txt": {Size: 500},
	}

	sortListing(in, names, ListOptions{Sort: SortSize}, stats)

	want := []string{"small.txt", "same-a.txt", "same-b.txt", "big.bin"}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("names[%d] = %q, want %q (order: %v)", i, names[i], w, names)
		}
	}
}

// A symlink is not a directory whatever it points at, so it sorts with the
// files rather than ahead of them.
func TestASymlinkSortsWithTheFiles(t *testing.T) {
	in := []vfs.DirEntry{
		{Name: "link", Kind: vfs.KindSymlink},
		{Name: "adir", Kind: vfs.KindDir},
	}
	names := namesOf(in)

	sortListing(in, names, ListOptions{}, nil)

	if names[0] != "adir" {
		t.Fatalf("the directory did not sort first: %v", names)
	}
	if in[1].Kind != vfs.KindSymlink {
		t.Fatalf("the symlink's kind did not follow it: %v", in[1].Kind)
	}
}

// Only size and mtime need a stat. Ordering by name or kind reads what the
// directory read already returned, which is what keeps a large listing at one
// syscall per page rather than one per file.
func TestOnlySizeAndMtimeNeedAStat(t *testing.T) {
	for _, c := range []struct {
		key  SortKey
		want bool
	}{
		{SortName, false}, {SortKind, false},
		{SortSize, true}, {SortMtime, true},
	} {
		if got := c.key.NeedsStat(); got != c.want {
			t.Errorf("SortKey(%d).NeedsStat() = %v, want %v", c.key, got, c.want)
		}
	}
}

// An unknown key is the default rather than a refusal: a listing is a read,
// and failing it over a spelling would take the folder away.
func TestAnUnknownSortKeyIsTheDefault(t *testing.T) {
	if got := ParseSortKey("nonsense"); got != SortName {
		t.Fatalf("an unknown key parsed as %v, want SortName", got)
	}
	if got := ParseSortKey(""); got != SortName {
		t.Fatalf("an empty key parsed as %v, want SortName", got)
	}
	for in, want := range map[string]SortKey{
		"name": SortName, "kind": SortKind, "size": SortSize, "mtime": SortMtime,
	} {
		if got := ParseSortKey(in); got != want {
			t.Errorf("ParseSortKey(%q) = %v, want %v", in, got, want)
		}
	}
}
