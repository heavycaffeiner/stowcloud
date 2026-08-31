package index

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

func openIndex(t *testing.T, dir string) *NameIndex {
	t.Helper()
	ix, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ix
}

func query(t *testing.T, ix *NameIndex, q string) Result {
	t.Helper()
	res, err := ix.Query([]byte(q), 0)
	if err != nil {
		t.Fatalf("Query(%q): %v", q, err)
	}
	return res
}

func hitPaths(res Result) []string {
	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.Path)
	}
	slices.Sort(out)
	return out
}

// A query is base plus the deltas minus the tombstones, and an append is
// visible immediately: that is why the segments are split at all.
func TestAppendIsVisibleToTheNextQuery(t *testing.T) {
	ix := openIndex(t, t.TempDir())

	if got := query(t, ix, "report"); len(got.Hits) != 0 {
		t.Errorf("an empty index answered %v", hitPaths(got))
	}

	if err := ix.Append([]Entry{{Share: 1, Path: "a/report.pdf"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := query(t, ix, "report")
	if want := []string{"a/report.pdf"}; !slices.Equal(hitPaths(got), want) {
		t.Errorf("got %v, want %v", hitPaths(got), want)
	}
	if got.MustFallBack() {
		t.Errorf("the index declined a query it could answer: %v", got.Fallback)
	}
}

// A tombstone hides a name from the moment it is written, before any merge
// lands.
func TestATombstoneHidesANameBeforeTheMerge(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if err := ix.Append([]Entry{
		{Share: 1, Path: "keep.txt"},
		{Share: 1, Path: "gone.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "gone.txt"}}); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}

	if got := hitPaths(query(t, ix, ".txt")); !slices.Equal(got, []string{"keep.txt"}) {
		t.Errorf("got %v, want only keep.txt", got)
	}
}

// A tombstone only hides a write that came before it, which is what makes a
// delete-then-recreate end up live rather than hidden forever.
func TestDeleteThenRecreateEndsUpLive(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	entry := []Entry{{Share: 1, Path: "again.txt"}}

	if err := ix.Append(entry); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone(entry); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	if got := query(t, ix, "again"); len(got.Hits) != 0 {
		t.Fatalf("the tombstone did not hide the name: %v", hitPaths(got))
	}
	if err := ix.Append(entry); err != nil {
		t.Fatalf("re-append: %v", err)
	}

	if got := hitPaths(query(t, ix, "again")); !slices.Equal(got, []string{"again.txt"}) {
		t.Errorf("a recreated name stayed hidden: %v", got)
	}
}

// A query under three bytes has no trigram to look up, and saying so is
// different from answering nothing.
func TestAShortQueryFallsBack(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if err := ix.Append([]Entry{{Share: 1, Path: "ab.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := query(t, ix, "ab")
	if got.Fallback != FallbackQueryTooShort || !got.MustFallBack() {
		t.Errorf("a two-byte query reported %v", got.Fallback)
	}
	if len(got.Hits) != 0 {
		t.Error("a declining index returned hits")
	}
}

// An index that reached its ceiling cannot tell "no such name" from "a name
// past where I stopped", so every query declines rather than answering from
// part of the tree with a success status.
func TestAnIncompleteIndexDeclinesEveryQuery(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if err := ix.Append([]Entry{{Share: 1, Path: "present.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := query(t, ix, "present"); got.MustFallBack() {
		t.Fatalf("a complete index declined: %v", got.Fallback)
	}

	ix.SetIncomplete(true)
	got := query(t, ix, "present")
	if got.Fallback != FallbackIncomplete {
		t.Errorf("an incomplete index reported %v, want FallbackIncomplete", got.Fallback)
	}
	if len(got.Hits) != 0 {
		t.Error("an incomplete index answered from what it holds")
	}

	// A rebuild clears it, or every query would walk forever.
	ix.SetIncomplete(false)
	if got := query(t, ix, "present"); got.MustFallBack() {
		t.Errorf("the flag did not clear: %v", got.Fallback)
	}
}

// Every trigram high-df pruned means the index cannot narrow the query at all.
// Saying so is what stops the caller treating an empty result as "nothing
// matched".
func TestAllTrigramsPrunedFallsBack(t *testing.T) {
	dir := t.TempDir()
	ix, err := Open(dir, Config{BlockSize: 1, PruneDFRatio: 0.1, MergeRatio: 0.15})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// One name per block, every one holding the same run, so its trigrams are
	// in every block and all of them prune.
	var corpus []Entry
	for i := range MinBlocksForPrune * 2 {
		corpus = append(corpus, Entry{Share: 1, Path: string(rune('a'+i)) + "-common"})
	}
	if err := ix.Append(corpus); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	got := query(t, ix, "common")
	if got.Fallback != FallbackAllTrigramsPruned {
		t.Errorf("reported %v, want FallbackAllTrigramsPruned", got.Fallback)
	}
}

// The merge collapses the overlay into a new base, and what it absorbed is
// gone from the segments while the names stay findable.
func TestMergeCollapsesTheOverlayAndKeepsTheNames(t *testing.T) {
	dir := t.TempDir()
	ix := openIndex(t, dir)

	var corpus []Entry
	for i := range 100 {
		corpus = append(corpus, Entry{Share: 1, Path: filepath.Join("d", "file-"+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt")})
	}
	if err := ix.Append(corpus); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !ix.NeedsMerge() {
		t.Fatal("an index with a delta and no base should want a merge")
	}

	before := hitPaths(query(t, ix, "file-"))
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	after := hitPaths(query(t, ix, "file-"))
	if !slices.Equal(before, after) {
		t.Errorf("the merge changed the answer: %d before, %d after", len(before), len(after))
	}

	st := ix.Stats()
	if st.DeltaEntries != 0 {
		t.Errorf("%d entries are still in the overlay after a merge", st.DeltaEntries)
	}
	if st.BaseEntries != uint64(len(corpus)) {
		t.Errorf("the base holds %d entries, want %d", st.BaseEntries, len(corpus))
	}
	if _, err := os.Stat(filepath.Join(dir, baseName)); err != nil {
		t.Errorf("no base segment on disk after a merge: %v", err)
	}
}

// A tombstone recorded during a build is newer than the base being published
// and has to survive the swap, or a deleted file comes back.
func TestMergeKeepsATombstoneNewerThanTheNewBase(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if err := ix.Append([]Entry{{Share: 1, Path: "doomed.txt"}, {Share: 1, Path: "safe.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "doomed.txt"}}); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatalf("second merge: %v", err)
	}

	if got := hitPaths(query(t, ix, ".txt")); !slices.Equal(got, []string{"safe.txt"}) {
		t.Errorf("got %v, want only safe.txt", got)
	}
}

// A merge that was told to stop must never damage the index: nothing is
// replaced until the new segment is complete.
func TestARefusedMergeChangesNothing(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if err := ix.Append([]Entry{{Share: 1, Path: "a.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	before := ix.Stats()

	if err := ix.Merge(t.Context(), func() bool { return false }); err != nil {
		t.Fatalf("a refused merge returned an error: %v", err)
	}
	after := ix.Stats()
	if after.BaseEntries != before.BaseEntries || after.DeltaEntries != before.DeltaEntries {
		t.Errorf("a refused merge changed the index: %+v then %+v", before, after)
	}
	if got := hitPaths(query(t, ix, "a.txt")); !slices.Equal(got, []string{"a.txt"}) {
		t.Errorf("a refused merge lost a name: %v", got)
	}
}

// Queries running during a merge see either the old set or the new one, never
// a mix, and never a torn read. Run under -race this is the concurrency proof.
func TestQueriesDuringAMergeSeeAConsistentSet(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	var corpus []Entry
	for i := range 400 {
		corpus = append(corpus, Entry{Share: 1, Path: "dir/report-" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".txt"})
	}
	if err := ix.Append(corpus); err != nil {
		t.Fatalf("Append: %v", err)
	}
	want := len(hitPaths(query(t, ix, "report")))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		task.Go(t.Context(), "index: query during a merge", func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := ix.Query([]byte("report"), 0)
				if err != nil {
					t.Errorf("query during a merge: %v", err)
					return
				}
				if !res.MustFallBack() && len(res.Hits) != want {
					t.Errorf("a query during a merge saw %d hits, want %d", len(res.Hits), want)
					return
				}
			}
		})
	}

	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Errorf("Merge: %v", err)
	}
	close(stop)
	wg.Wait()

	if got := len(hitPaths(query(t, ix, "report"))); got != want {
		t.Errorf("after the merge %d hits, want %d", got, want)
	}
}

// A second merge cannot start from a snapshot the first has not published, or
// whichever finished last would publish a base missing the other's writes.
func TestConcurrentMergesDoNotLoseWrites(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	var corpus []Entry
	for i := range 200 {
		corpus = append(corpus, Entry{Share: 1, Path: "f-" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".txt"})
	}
	if err := ix.Append(corpus); err != nil {
		t.Fatalf("Append: %v", err)
	}
	want := len(hitPaths(query(t, ix, "f-")))

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		task.Go(t.Context(), "index: concurrent merge", func() {
			defer wg.Done()
			if err := ix.Merge(context.Background(), nil); err != nil {
				t.Errorf("concurrent merge: %v", err)
			}
		})
	}
	wg.Wait()

	if got := len(hitPaths(query(t, ix, "f-"))); got != want {
		t.Errorf("concurrent merges lost writes: %d hits, want %d", got, want)
	}
}

// The overlay survives a restart, and so does the fact that an index reached
// its ceiling: the flag is derived on open rather than stored twice.
func TestReopenRecoversTheOverlay(t *testing.T) {
	dir := t.TempDir()
	ix := openIndex(t, dir)
	if err := ix.Append([]Entry{{Share: 1, Path: "kept.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "removed.txt"}}); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}

	reopened := openIndex(t, dir)
	if got := hitPaths(query(t, reopened, "kept")); !slices.Equal(got, []string{"kept.txt"}) {
		t.Errorf("the overlay did not survive a reopen: %v", got)
	}
	if st := reopened.Stats(); st.Tombstones != 1 {
		t.Errorf("%d tombstones after a reopen, want 1", st.Tombstones)
	}
}

// A torn delta tail is cut on open rather than disabling the index, which is
// what stops a power loss mid-append from costing the whole cache.
func TestOpenRecoversATornDelta(t *testing.T) {
	dir := t.TempDir()
	ix := openIndex(t, dir)
	if err := ix.Append([]Entry{{Share: 1, Path: "kept.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := filepath.Join(dir, "delta.000.idx")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, werr := f.Write([]byte{0x40, 0x00, 0x00, 0x00, 0xde, 0xad}); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	reopened := openIndex(t, dir)
	if got := hitPaths(query(t, reopened, "kept")); !slices.Equal(got, []string{"kept.txt"}) {
		t.Errorf("the intact prefix did not survive: %v", got)
	}
	rec, err := ReadRecords(path)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if rec.Torn {
		t.Error("the torn tail was not cut on open")
	}
}

// ChildrenOf answers with the overlay applied, so two updates in a row agree
// with each other. Direct children only: a subtree answer would make one file
// at the top of a share cost the whole share.
func TestChildrenOfIsDirectChildrenWithTheOverlayApplied(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if err := ix.Append([]Entry{
		{Share: 1, Path: "d/a.txt"},
		{Share: 1, Path: "d/b.txt"},
		{Share: 1, Path: "d/deep/c.txt"},
		{Share: 1, Path: "other.txt"},
		{Share: 2, Path: "d/elsewhere.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := ix.ChildrenOf(1, "d")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if want := []string{"d/a.txt", "d/b.txt"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if terr := ix.Tombstone([]Entry{{Share: 1, Path: "d/a.txt"}}); terr != nil {
		t.Fatalf("Tombstone: %v", terr)
	}
	got, err = ix.ChildrenOf(1, "d")
	if err != nil {
		t.Fatalf("ChildrenOf after a tombstone: %v", err)
	}
	if want := []string{"d/b.txt"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// The share root names its own direct children and nothing deeper.
	root, err := ix.ChildrenOf(1, "")
	if err != nil {
		t.Fatalf("ChildrenOf at the root: %v", err)
	}
	if want := []string{"other.txt"}; !slices.Equal(root, want) {
		t.Errorf("at the root got %v, want %v", root, want)
	}
}

// The gate bounds read cost: the linear delta scan can never grow past a fixed
// fraction of the base.
func TestNeedsMergeFollowsTheRatio(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if ix.NeedsMerge() {
		t.Error("an empty index wants a merge")
	}
	if err := ix.Append([]Entry{{Share: 1, Path: "a.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !ix.NeedsMerge() {
		t.Error("an index with a delta and no base should want a merge")
	}
	if err := ix.Merge(t.Context(), nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if ix.NeedsMerge() {
		t.Error("a freshly merged index still wants a merge")
	}
}

func TestAppendAndTombstoneOfNothingAreNoOps(t *testing.T) {
	ix := openIndex(t, t.TempDir())
	if err := ix.Append(nil); err != nil {
		t.Errorf("Append(nil): %v", err)
	}
	if err := ix.Tombstone(nil); err != nil {
		t.Errorf("Tombstone(nil): %v", err)
	}
	if st := ix.Stats(); st.Entries != 0 || st.Tombstones != 0 {
		t.Errorf("empty writes changed the index: %+v", st)
	}
}

func TestConfigAndDirAreReportedBack(t *testing.T) {
	dir := t.TempDir()
	ix := openIndex(t, dir)
	if ix.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", ix.Dir(), dir)
	}
	if got := ix.Config(); got != DefaultConfig() {
		t.Errorf("Config() = %+v, want the default", got)
	}
	// A zero block size takes the default rather than writing a segment that
	// cannot be opened.
	other, err := Open(t.TempDir(), Config{})
	if err != nil {
		t.Fatalf("Open with a zero config: %v", err)
	}
	if other.Config().BlockSize != DefaultConfig().BlockSize {
		t.Errorf("a zero block size stayed zero")
	}
}

func TestFallbackReasonNames(t *testing.T) {
	cases := map[FallbackReason]string{
		FallbackNone:              "-",
		FallbackQueryTooShort:     "QueryTooShort",
		FallbackAllTrigramsPruned: "AllTrigramsPruned",
		FallbackIncomplete:        "Incomplete",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", r, got, want)
		}
	}
}
