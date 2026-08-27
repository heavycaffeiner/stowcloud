package core

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

func TestParseSortKeyMapsTheNamedSpellings(t *testing.T) {
	cases := []struct {
		in   string
		want SortKey
	}{
		{"kind", SortKind},
		{"size", SortSize},
		{"mtime", SortMtime},
		{"name", SortName},
		{"", SortName},
		{"Size", SortName},
		{"created", SortName},
		{"size ", SortName},
	}
	for _, tc := range cases {
		t.Run("in="+tc.in, func(t *testing.T) {
			if got := ParseSortKey(tc.in); got != tc.want {
				t.Fatalf("ParseSortKey(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestNeedsStatIsTrueForExactlyTheTwoStatKeys(t *testing.T) {
	cases := []struct {
		name string
		key  SortKey
		want bool
	}{
		{"name", SortName, false},
		{"kind", SortKind, false},
		{"size", SortSize, true},
		{"mtime", SortMtime, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.key.NeedsStat(); got != tc.want {
				t.Fatalf("%s.NeedsStat() = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// The zero ListOptions is what every caller that does not care passes, and
// it has to mean the cheap listing: by name, ascending, one default page.
func TestTheZeroListOptionsIsTheDefaultListing(t *testing.T) {
	var opt ListOptions
	if opt.Sort != SortName {
		t.Fatalf("the zero ListOptions sorts by %d, want SortName", opt.Sort)
	}
	if opt.Desc {
		t.Fatal("the zero ListOptions is descending")
	}
	if opt.Limit != 0 {
		t.Fatalf("the zero ListOptions limits to %d, want 0", opt.Limit)
	}
	if opt.Sort.NeedsStat() {
		t.Fatal("the default key stats every entry; it is meant to cost nothing")
	}
}

// The empty cursor is the first page, which is what makes the zero value a
// usable argument rather than an error.
func TestTheZeroCursorIsTheFirstPage(t *testing.T) {
	var cur Cursor
	if cur != "" {
		t.Fatalf("the zero Cursor is %q, want empty", cur)
	}
}

func TestPageSizeCeilingIsAboveTheDefault(t *testing.T) {
	if pageSize != 200 {
		t.Fatalf("pageSize = %d, want 200", pageSize)
	}
	if maxPageSize != 2000 {
		t.Fatalf("maxPageSize = %d, want 2000", maxPageSize)
	}
	if maxPageSize <= pageSize {
		t.Fatalf("the ceiling %d is not above the default %d", maxPageSize, pageSize)
	}
}

// Entry.IsDir is Kind.IsDir() and nothing more. The entries themselves are
// built in the listing step, so the invariant is asserted here over every
// kind the VFS defines.
func TestIsDirIsExactlyKindIsDirForEveryKind(t *testing.T) {
	for _, kind := range []vfs.Kind{vfs.KindOther, vfs.KindFile, vfs.KindDir, vfs.KindSymlink} {
		e := Entry{Kind: kind, IsDir: kind.IsDir()}
		if e.IsDir != (e.Kind == vfs.KindDir) {
			t.Fatalf("kind %s: IsDir = %v", kind, e.IsDir)
		}
	}
}

func TestASymlinkIsNeverADirectory(t *testing.T) {
	e := Entry{Kind: vfs.KindSymlink, IsDir: vfs.KindSymlink.IsDir()}
	if e.IsDir {
		t.Fatal("a symlink reported itself a directory; under the default policy it cannot be entered")
	}
}

// BTimeNs is a pointer so that "this filesystem reports no birth time" is a
// different fact from "the birth time is the epoch".
func TestEntryTellsAnAbsentBirthTimeFromAnEpochOne(t *testing.T) {
	epoch := int64(0)
	absent := Entry{}
	present := Entry{BTimeNs: &epoch}
	if absent.BTimeNs != nil {
		t.Fatal("the zero Entry claims a birth time")
	}
	if present.BTimeNs == nil || *present.BTimeNs != 0 {
		t.Fatalf("an epoch birth time did not survive: %v", present.BTimeNs)
	}
}
