package index

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// A merge builds outside the lock, so what has to be proved is that writes
// landing during one are not lost.
//
// The old shape held the write lock for the whole rebuild, which was correct
// by construction and cost 4.4 seconds of blocked queries on a million entries.
// This one is faster and has a way to be wrong, so every way it could be is a
// test.

// entryPaths is what the index holds for a share, base and overlay together.
func entryPaths(t *testing.T, ix *NameIndex, share uint32) []string {
	t.Helper()
	out, err := ix.ChildrenOf(share, "d")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	return out
}

// An append during the build survives the swap. It is not in the new base,
// because the snapshot predates it; it has to still be in the overlay.
func TestAnAppendDuringAMergeIsNotLost(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "d/before.txt"}}); err != nil {
		t.Fatal(err)
	}

	// The seal is what a merge does first. Between it and the publish is the
	// window this test is about.
	snap, ok := ix.sealForMerge()
	if !ok {
		t.Fatal("the seal was refused with no merge running")
	}
	if err := ix.Append([]Entry{{Share: 1, Path: "d/during.txt"}}); err != nil {
		t.Fatal(err)
	}
	buf, berr := snap.build(t.Context(), nil, ix.cfg)
	if berr != nil {
		t.Fatal(berr)
	}
	if perr := ix.publish(snap, buf); perr != nil {
		t.Fatal(perr)
	}
	ix.releaseMerge()

	got := entryPaths(t, ix, 1)
	want := []string{"d/before.txt", "d/during.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("after the merge the index holds %v, want %v", got, want)
	}

	// And the file it landed in still exists: removing every sealed segment
	// must not remove the one opened after the seal.
	if err := ix.Append([]Entry{{Share: 1, Path: "d/after.txt"}}); err != nil {
		t.Fatal(err)
	}
	if got := entryPaths(t, ix, 1); len(got) != 3 {
		t.Fatalf("the index holds %v after a later append, want three entries", got)
	}
}

// A tombstone recorded during the build is newer than the base being written,
// so it has to survive. Dropping it resurrects a deleted file.
func TestATombstoneDuringAMergeSurvives(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "d/one.txt"},
		{Share: 1, Path: "d/two.txt"},
	}); err != nil {
		t.Fatal(err)
	}

	snap, ok := ix.sealForMerge()
	if !ok {
		t.Fatal("the seal was refused")
	}
	// Deleted after the snapshot, so the new base still contains it.
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "d/one.txt"}}); err != nil {
		t.Fatal(err)
	}
	buf, berr := snap.build(t.Context(), nil, ix.cfg)
	if berr != nil {
		t.Fatal(berr)
	}
	if perr := ix.publish(snap, buf); perr != nil {
		t.Fatal(perr)
	}
	ix.releaseMerge()

	got := entryPaths(t, ix, 1)
	if !slices.Equal(got, []string{"d/two.txt"}) {
		t.Fatalf("the index holds %v, want only the file that was not deleted", got)
	}

	// And it survives a reopen, which is the half that lives on disk: the
	// tombstone file is rewritten rather than removed for exactly this.
	reopened, err := Open(ix.dir, DefaultConfig())
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if got := entryPaths(t, reopened, 1); !slices.Equal(got, []string{"d/two.txt"}) {
		t.Fatalf("after a reopen the index holds %v, so the deletion did not survive", got)
	}
}

// A tombstone the merge already applied is dropped, which is the point of
// merging: an overlay that only grows is one every query keeps scanning.
func TestAnAppliedTombstoneIsDropped(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "d/one.txt"},
		{Share: 1, Path: "d/two.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "d/one.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	if s := ix.Stats(); s.Tombstones != 0 || s.TombBytes != 0 {
		t.Fatalf("the merge kept %d tombstones in %d bytes, want none", s.Tombstones, s.TombBytes)
	}
	if got := entryPaths(t, ix, 1); !slices.Equal(got, []string{"d/two.txt"}) {
		t.Fatalf("the index holds %v", got)
	}
}

// Two merges at once would each build from a snapshot taken before the other's
// publish, and the second to finish would write a base missing the first's
// writes. The second is refused instead.
func TestASecondMergeIsRefusedWhileOneIsRunning(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "d/one.txt"}}); err != nil {
		t.Fatal(err)
	}

	snap, ok := ix.sealForMerge()
	if !ok {
		t.Fatal("the first seal was refused")
	}
	if _, second := ix.sealForMerge(); second {
		t.Fatal("a second merge started while one was in flight")
	}

	buf, berr := snap.build(t.Context(), nil, ix.cfg)
	if berr != nil {
		t.Fatal(berr)
	}
	if perr := ix.publish(snap, buf); perr != nil {
		t.Fatal(perr)
	}
	ix.releaseMerge()

	// And one can start again afterwards.
	if _, third := ix.sealForMerge(); !third {
		t.Fatal("a merge could not start after the previous one finished")
	}
	ix.releaseMerge()
}

// The whole point of the change: queries answer while a merge runs. Under the
// old shape this test would deadlock against its own write lock rather than
// fail, so it is written to complete rather than to time itself.
func TestQueriesAnswerWhileAMergeBuilds(t *testing.T) {
	ix := newIndex(t)
	var entries []Entry
	for i := range 2000 {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("d/file-%04d.txt", i)})
	}
	if err := ix.Append(entries); err != nil {
		t.Fatal(err)
	}

	snap, ok := ix.sealForMerge()
	if !ok {
		t.Fatal("the seal was refused")
	}

	// With the build in progress, both a read and a write have to complete.
	// Neither takes the lock the build no longer holds.
	if r, err := ix.Query([]byte("file-0500"), 10); err != nil || len(r.Hits) == 0 {
		t.Fatalf("a query during a merge returned %d hits, err=%v", len(r.Hits), err)
	}
	if err := ix.Append([]Entry{{Share: 1, Path: "d/during.txt"}}); err != nil {
		t.Fatalf("an append during a merge: %v", err)
	}

	buf, berr := snap.build(t.Context(), nil, ix.cfg)
	if berr != nil {
		t.Fatal(berr)
	}
	if perr := ix.publish(snap, buf); perr != nil {
		t.Fatal(perr)
	}
	ix.releaseMerge()

	if r, err := ix.Query([]byte("during"), 10); err != nil || len(r.Hits) != 1 {
		t.Fatalf("the entry written during the merge is not queryable: %d hits, err=%v", len(r.Hits), err)
	}
}

// The concurrent case, for the race detector: readers, writers and a merge at
// once, with every write accounted for at the end.
func TestNothingIsLostUnderConcurrentWritesAndMerges(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "d/seed.txt"}}); err != nil {
		t.Fatal(err)
	}

	const writes = 200
	var wg sync.WaitGroup

	wg.Add(2)
	task.Go(t.Context(), "index writer under test", func() {
		defer wg.Done()
		for i := range writes {
			if err := ix.Append([]Entry{{Share: 1, Path: fmt.Sprintf("d/w-%04d.txt", i)}}); err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
			if i%50 == 0 {
				if merr := ix.Merge(t.Context(), nil); merr != nil {
					t.Errorf("merge at %d: %v", i, merr)
					return
				}
			}
		}
	})
	task.Go(t.Context(), "index reader under test", func() {
		defer wg.Done()
		for range writes {
			if _, err := ix.Query([]byte("seed"), 10); err != nil {
				t.Errorf("query: %v", err)
				return
			}
		}
	})

	wg.Wait()

	// One final merge, so anything still in the overlay is folded in and the
	// count is of the base as well as the overlay.
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	got := entryPaths(t, ix, 1)
	if len(got) != writes+1 {
		t.Fatalf("the index holds %d entries, want %d: a write was lost across a merge",
			len(got), writes+1)
	}
}
