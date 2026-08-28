//go:build linux

package svc

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Build then query: the round trip that moves a deployment from the walk tier
// to the index tier.
func TestBuildThenQueryRoundTrip(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "report.pdf", "d/report-2024.pdf", "d/e/notes.txt")

	progress, err := svc.Build(t.Context(), []search.Source{src}, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if progress.Files != 3 {
		t.Errorf("indexed %d files, want 3", progress.Files)
	}
	if progress.Dirs < 3 {
		t.Errorf("visited %d directories, want at least 3", progress.Dirs)
	}
	if progress.Partial {
		t.Error("a small corpus reported itself partial")
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "report"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Tier != TierIndex {
		t.Errorf("tier is %v, want index", res.Tier)
	}
	want := []string{"d/report-2024.pdf", "report.pdf"}
	if !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("got %v, want %v", hitPaths(res.Hits), want)
	}
}

// Directories are not indexed: the build indexes files and descends through
// directories, so a name in the index always resolves to a file.
func TestBuildIndexesFilesAndNotDirectories(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "dir/inside.txt")

	if _, err := svc.Build(t.Context(), []search.Source{src}, nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := ix.Stats().Entries; got != 1 {
		t.Errorf("the index holds %d entries, want only the file", got)
	}
}

// Building with no index open is a refusal rather than a silent no-op.
func TestBuildWithoutAnIndexRefuses(t *testing.T) {
	svc := New(Options{})
	if _, err := svc.Build(t.Context(), nil, nil, nil); !errors.Is(err, ErrNoIndex) {
		t.Errorf("Build with no index = %v, want ErrNoIndex", err)
	}
}

// The gate ends the build, and what was appended is real and stays: the index
// is allowed to hold less than the corpus because a miss falls back to a walk.
func TestARefusedGateStopsTheBuildAndKeepsWhatItHad(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "a.txt", "b.txt")

	progress, err := svc.Build(t.Context(), []search.Source{src}, func() bool { return false }, nil)
	if err != nil {
		t.Fatalf("a gated build returned an error: %v", err)
	}
	if progress.Files != 0 {
		t.Errorf("a build refused at the first directory indexed %d files", progress.Files)
	}
}

// A rebuild clears the incomplete flag, or an index that once hit its ceiling
// would make every query walk forever.
func TestBuildClearsTheIncompleteFlag(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "a.txt")
	ix.SetIncomplete(true)

	if _, err := svc.Build(t.Context(), []search.Source{src}, nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ix.Incomplete() {
		t.Error("a completed build left the index marked incomplete")
	}
}

// Reaching the ceiling marks the index short of its corpus, so every query
// declines rather than returning a result missing the rest with a success
// status.
func TestBuildAtTheCeilingReportsPartialAndMarksIncomplete(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "a.txt", "b.txt", "c.txt", "d.txt")

	// The production ceiling is five million; a test sets a small one rather
	// than building that many files.
	b := &builder{ix: ix, ceiling: 2, batch: make([]index.Entry, 0, buildBatch)}
	done, err := b.walkSource(t.Context(), src)
	if err != nil {
		t.Fatalf("walkSource: %v", err)
	}
	if !done || !b.progress.Partial {
		t.Errorf("the ceiling did not stop the build: done %v, partial %v", done, b.progress.Partial)
	}
	if ferr := b.flush(); ferr != nil {
		t.Fatalf("flush: %v", ferr)
	}
	if !ix.Incomplete() {
		t.Fatal("reaching the ceiling did not mark the index incomplete")
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: ".txt"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Fallback != index.FallbackIncomplete {
		t.Errorf("fallback is %v, want Incomplete", res.Fallback)
	}
	// And the walk still finds every file, which is why declining is safe.
	if len(res.Hits) != 4 {
		t.Errorf("the walk found %d files, want all 4", len(res.Hits))
	}
}

// Progress arrives while the build runs, so a lengthy one is observable instead
// of looking like a request that never answered.
func TestBuildReportsProgress(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "a.txt", "b.txt")

	var reports int
	if _, err := svc.Build(t.Context(), []search.Source{src}, nil, func(BuildProgress) {
		reports++
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if reports == 0 {
		t.Error("a build with a reporter reported nothing")
	}
}

// A share with no root is skipped rather than failing the whole build.
func TestBuildSkipsABrokenShare(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	good, _ := corpus(t, 1, "a.txt")
	broken := search.Source{Share: 2}

	progress, err := svc.Build(t.Context(), []search.Source{broken, good}, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if progress.Files != 1 {
		t.Errorf("indexed %d files, want the one reachable file", progress.Files)
	}
}

// A create event lands in the index.
func TestUpdateIndexesANewFile(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, dir := corpus(t, 1, "existing.txt")
	sources := func() []search.Source { return []search.Source{src} }
	u := NewUpdater(svc, sources, quietLogger())

	if _, err := svc.Build(t.Context(), sources(), nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// A file arrives after the build, which is exactly the silent-staleness
	// case the updater exists for.
	newFile := filepath.Join(dir, "arrived.txt")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := u.reconcile(t.Context(), ix, src, ""); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	children, err := ix.ChildrenOf(1, "")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if !slices.Contains(children, "arrived.txt") {
		t.Errorf("the new file is not in the index: %v", children)
	}
}

// A delete is tombstoned, because an entry nothing removes is one the next
// merge writes into the base and every query keeps scanning.
func TestUpdateTombstonesADeletedFile(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, dir := corpus(t, 1, "keep.txt", "remove.txt")
	sources := func() []search.Source { return []search.Source{src} }
	u := NewUpdater(svc, sources, quietLogger())

	if _, err := svc.Build(t.Context(), sources(), nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "remove.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := u.reconcile(t.Context(), ix, src, ""); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	children, err := ix.ChildrenOf(1, "")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if slices.Contains(children, "remove.txt") {
		t.Errorf("the deleted file is still indexed: %v", children)
	}
	if !slices.Contains(children, "keep.txt") {
		t.Errorf("reconcile dropped a file that is still there: %v", children)
	}
}

// Reconciling an unchanged directory writes nothing: re-appending a listing on
// every touch would grow the overlay until the merge is all the index does.
func TestReconcileWritesNothingWhenNothingChanged(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "a.txt", "b.txt")
	u := NewUpdater(svc, func() []search.Source { return []search.Source{src} }, quietLogger())

	if _, err := svc.Build(t.Context(), []search.Source{src}, nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	before := ix.Stats()
	for range 3 {
		if err := u.reconcile(t.Context(), ix, src, ""); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
	after := ix.Stats()
	if after.DeltaEntries != before.DeltaEntries || after.Tombstones != before.Tombstones {
		t.Errorf("reconciling an unchanged directory wrote: %+v then %+v", before, after)
	}
}

// A full queue drops without blocking the watcher. The watcher feeds the
// change channel every connected client reads, so an index update must never
// be what makes a listing go stale in a browser.
func TestAFullQueueDropsWithoutBlocking(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "dropped.txt")
	u := NewUpdater(svc, func() []search.Source { return []search.Source{src} }, quietLogger())

	// Fill the queue, then offer well past it. Offer must return either way.
	for i := range updateQueue + 16 {
		u.Offer(Change{Share: 1, Dir: string(rune('a' + i%26))})
	}
	if len(u.queue) != updateQueue {
		t.Errorf("the queue holds %d, want it capped at %d", len(u.queue), updateQueue)
	}
}

// The second link of the chain a drop depends on: the walk tier is always
// current, so a file the index never received is still found whenever the
// index is not the one answering.
func TestTheWalkFindsAFileTheIndexNeverReceived(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "dropped.txt")

	// The index holds nothing for this file, as it would after a dropped
	// update, and it is short of its corpus, so it declines and the walk runs.
	ix.SetIncomplete(true)

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "dropped"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Tier != TierWalk {
		t.Errorf("tier is %v, want walk", res.Tier)
	}
	if len(res.Hits) != 1 {
		t.Errorf("the walk did not find a file the index never got: %v", hitPaths(res.Hits))
	}
}

// The third link: a hit the index still names for a deleted file is dropped by
// the stat before it reaches the caller.
func TestAStaleEntryForADeletedFileIsHiddenByRevalidation(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, dir := corpus(t, 1, "vanishes.txt")

	if _, err := svc.Build(t.Context(), []search.Source{src}, nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The file goes away and no update is ever applied, which is exactly the
	// state a dropped event leaves behind.
	if err := os.Remove(filepath.Join(dir, "vanishes.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "vanishes"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("a stale entry for a deleted file surfaced: %v", hitPaths(res.Hits))
	}
}

// Removals still apply at the ceiling: a bound that stops removals is one that
// can only grow.
func TestAtTheCeilingRemovalsStillApplyAndAdditionsDoNot(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, dir := corpus(t, 1, "old.txt")
	u := NewUpdater(svc, func() []search.Source { return []search.Source{src} }, quietLogger())
	u.entryCeiling = 1

	if _, err := svc.Build(t.Context(), []search.Source{src}, nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !u.full(ix) {
		t.Fatal("the fixture did not reach its ceiling")
	}

	// A new file at the ceiling is not indexed, and the index says it is short.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := u.reconcile(t.Context(), ix, src, ""); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !ix.Incomplete() {
		t.Error("an index at its ceiling was not marked incomplete")
	}

	// A removal still lands.
	if err := os.Remove(filepath.Join(dir, "old.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := u.reconcile(t.Context(), ix, src, ""); err != nil {
		t.Fatalf("reconcile after a removal: %v", err)
	}
	children, err := ix.ChildrenOf(1, "")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if slices.Contains(children, "old.txt") {
		t.Errorf("a removal at the ceiling was refused: %v", children)
	}
}

// A directory that cannot be read is treated as empty, which tombstones what
// the index held. That is right for the common cause, a deleted directory.
func TestReconcileOfADeletedDirectoryTombstonesItsEntries(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, dir := corpus(t, 1, "sub/a.txt", "sub/b.txt")
	u := NewUpdater(svc, func() []search.Source { return []search.Source{src} }, quietLogger())

	if _, err := svc.Build(t.Context(), []search.Source{src}, nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := u.reconcile(t.Context(), ix, src, "sub"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	children, err := ix.ChildrenOf(1, "sub")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if len(children) != 0 {
		t.Errorf("a deleted directory still has indexed children: %v", children)
	}
}

// A path the validator rejects is not an error. The watcher relays what the
// kernel reported, and the kernel holds no view on this server's rules.
func TestReconcileOfAnIllegalPathIsNotAnError(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "a.txt")
	u := NewUpdater(svc, func() []search.Source { return []search.Source{src} }, quietLogger())

	if err := u.reconcile(t.Context(), ix, src, "../outside"); err != nil {
		t.Errorf("an illegal path produced an error: %v", err)
	}
}

// An event for a share the service does not have is dropped rather than
// panicking, and so is one arriving after the index was switched off.
func TestApplyIgnoresUnknownSharesAndAMissingIndex(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "a.txt")
	u := NewUpdater(svc, func() []search.Source { return []search.Source{src} }, quietLogger())

	u.apply(t.Context(), Change{Share: 99, Dir: ""})
	// The "events were lost" case leaves the index alone rather than dropping
	// it: a stale index still answers most queries.
	u.apply(t.Context(), Change{Share: 1, All: true})

	svc.SetIndex(nil)
	u.apply(t.Context(), Change{Share: 1, Dir: ""})
}

// The three ladder cases produce three distinct behaviours, which is the whole
// point of classifying them: an operator reading the log can tell which world
// they are in.
func TestOpenIndexDegradationLadder(t *testing.T) {
	t.Run("absent is quiet", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "never-built")
		ix, state := OpenIndex(dir, index.DefaultConfig(), quietLogger())
		if state != OpenAbsent {
			t.Errorf("state is %v, want absent", state)
		}
		if ix == nil {
			t.Error("a never-built index should still open empty")
		}
	})

	t.Run("an existing index opens ready", func(t *testing.T) {
		dir := t.TempDir()
		first, state := OpenIndex(dir, index.DefaultConfig(), quietLogger())
		if first == nil {
			t.Fatalf("first open failed with state %v", state)
		}
		if err := first.Append([]index.Entry{{Share: 1, Path: "a.txt"}}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := first.Merge(t.Context(), nil); err != nil {
			t.Fatalf("Merge: %v", err)
		}

		again, state := OpenIndex(dir, index.DefaultConfig(), quietLogger())
		if state != OpenReady || again == nil {
			t.Errorf("reopening a built index gave state %v", state)
		}
	})

	t.Run("corrupt disables and leaves the evidence", func(t *testing.T) {
		dir := t.TempDir()
		basePath := filepath.Join(dir, "base.idx")
		if err := os.WriteFile(basePath, []byte("not a segment at all, but long enough to be read"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		ix, state := OpenIndex(dir, index.DefaultConfig(), quietLogger())
		if state != OpenCorrupt {
			t.Errorf("state is %v, want corrupt", state)
		}
		if ix != nil {
			t.Error("a corrupt index was returned rather than disabled")
		}
		// The evidence stays on disk: removing it is the rebuild's job, and an
		// operator asking why search got slow wants it there.
		if _, err := os.Stat(basePath); err != nil {
			t.Errorf("the corrupt segment was removed: %v", err)
		}
	})

	t.Run("unreadable degrades without suggesting a rebuild", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores the permission this case depends on")
		}
		parent := t.TempDir()
		dir := filepath.Join(parent, "names")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() {
			// Put the bit back so the framework can clean up its own directory.
			if cerr := os.Chmod(dir, 0o700); cerr != nil {
				t.Errorf("restoring the directory mode: %v", cerr)
			}
		})

		ix, state := OpenIndex(dir, index.DefaultConfig(), quietLogger())
		if state != OpenUnavailable {
			t.Errorf("state is %v, want unavailable", state)
		}
		if ix != nil {
			t.Error("an unreadable index was returned")
		}
	})
}

func TestOpenStateNames(t *testing.T) {
	cases := map[OpenState]string{
		OpenReady:       "ready",
		OpenAbsent:      "absent",
		OpenCorrupt:     "corrupt",
		OpenUnavailable: "unavailable",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}
