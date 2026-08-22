package index

import (
	"fmt"
	"slices"
	"testing"
)

// ChildrenOf is what an incremental update compares a directory's real listing
// against. Every property here is one the update depends on being true.

func children(t *testing.T, ix *NameIndex, share uint32, dir string) []string {
	t.Helper()
	out, err := ix.ChildrenOf(share, dir)
	if err != nil {
		t.Fatalf("ChildrenOf(%d, %q): %v", share, dir, err)
	}
	return out
}

// Direct children, not the subtree. A subtree answer would make one file
// appearing at the top of a share cost a comparison against the whole share.
func TestChildrenOfIsOneLevelDeep(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "a/one.txt"},
		{Share: 1, Path: "a/two.txt"},
		{Share: 1, Path: "a/deeper/three.txt"},
		{Share: 1, Path: "b/four.txt"},
		{Share: 1, Path: "top.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := children(t, ix, 1, "a")
	want := []string{"a/one.txt", "a/two.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("ChildrenOf(a) = %v, want %v", got, want)
	}

	if got := children(t, ix, 1, ""); !slices.Equal(got, []string{"top.txt"}) {
		t.Fatalf("the share root's children = %v, want [top.txt]", got)
	}
}

// A share is a namespace. Two shares holding the same path is the normal case,
// and answering with the other one's entries would tombstone live files.
func TestChildrenOfDoesNotCrossShares(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "docs/one.txt"},
		{Share: 2, Path: "docs/two.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if got := children(t, ix, 1, "docs"); !slices.Equal(got, []string{"docs/one.txt"}) {
		t.Fatalf("share 1 = %v, want [docs/one.txt]", got)
	}
	if got := children(t, ix, 2, "docs"); !slices.Equal(got, []string{"docs/two.txt"}) {
		t.Fatalf("share 2 = %v, want [docs/two.txt]", got)
	}
}

// The overlay has to be applied, or two updates in a row disagree: the first
// appends a file and the second, not seeing it, appends it again.
func TestChildrenOfAppliesTheOverlay(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "a/one.txt"},
		{Share: 1, Path: "a/two.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "a/one.txt"}}); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	if err := ix.Append([]Entry{{Share: 1, Path: "a/three.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := children(t, ix, 1, "a")
	want := []string{"a/three.txt", "a/two.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("ChildrenOf = %v, want %v", got, want)
	}
}

// The base segment is the case that matters for cost: after a merge the
// entries are in blocks, and the answer has to come from the block-level
// search rather than from reading the segment.
func TestChildrenOfReadsTheBaseSegment(t *testing.T) {
	ix := newIndex(t)

	// More than one block's worth, so the search has blocks to skip and the
	// run genuinely straddles a boundary.
	var entries []Entry
	for i := range 500 {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("bulk/%03d.txt", i)})
	}
	entries = append(entries,
		Entry{Share: 1, Path: "target/one.txt"},
		Entry{Share: 1, Path: "target/two.txt"},
		Entry{Share: 1, Path: "target/nested/deep.txt"},
		Entry{Share: 1, Path: "zzz/last.txt"},
	)
	if err := ix.Append(entries); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	got := children(t, ix, 1, "target")
	want := []string{"target/one.txt", "target/two.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("ChildrenOf after a merge = %v, want %v", got, want)
	}

	// The first block's own run, which the block-level search reaches by
	// starting one block early rather than at the block it found.
	if got := children(t, ix, 1, "bulk"); len(got) != 500 {
		t.Fatalf("the first directory's children = %d entries, want 500", len(got))
	}
}

// The run can start partway through a block, so the search has to begin one
// block before the one it landed on. Landing on the block whose first entry is
// already past the directory loses everything before it.
func TestChildrenOfFindsARunThatStartsMidBlock(t *testing.T) {
	ix := newIndex(t)

	var entries []Entry
	// The block size is 32 by default, so this places the target's first entry
	// at a position that is not a block boundary, whatever the boundary is.
	for i := range 100 {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("aaa/%03d.txt", i)})
	}
	for i := range 100 {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("mmm/%03d.txt", i)})
	}
	for i := range 100 {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("zzz/%03d.txt", i)})
	}
	if err := ix.Append(entries); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Every one of the three, including the first and the last, because the
	// failure modes differ at each end.
	for _, dir := range []string{"aaa", "mmm", "zzz"} {
		if got := children(t, ix, 1, dir); len(got) != 100 {
			t.Errorf("ChildrenOf(%s) = %d entries, want 100", dir, len(got))
		}
	}
}

// A directory with nothing in it is an empty answer, not the parent's entries.
// The update tombstones what this returns, so a wrong answer here deletes live
// files from the index.
func TestChildrenOfAnUnknownDirectoryIsEmpty(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "a/one.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := children(t, ix, 1, "nowhere"); len(got) != 0 {
		t.Fatalf("an unknown directory returned %v, want nothing", got)
	}
	// A prefix of a real directory's name is not that directory: "a" must not
	// answer for "ab".
	if err := ix.Append([]Entry{{Share: 1, Path: "ab/two.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := children(t, ix, 1, "a"); !slices.Equal(got, []string{"a/one.txt"}) {
		t.Fatalf("ChildrenOf(a) = %v, want only a/one.txt", got)
	}
}
