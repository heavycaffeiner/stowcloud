//go:build linux

package svc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
)

// The equivalence test compares the tiers against one index instance held in
// memory for the length of the test. That leaves the part a running deployment
// actually depends on unasserted: the index is a directory of files, it is
// built once and read by every later process, and a merge rewrites those files
// underneath it.
//
// So the questions here are the ones a restart asks. Does a built index put
// anything on disk? Does reopening that directory in a fresh instance answer
// the same queries? Does it still agree with the walk after a merge has
// rewritten the segments? And does deleting the directory, which the design
// says is safe because the index is a cache, actually leave search working?

// buildOn indexes a corpus into a caller-supplied directory, so the same
// directory can be reopened afterwards.
func buildOn(t *testing.T, dir string, src search.Source) *Service {
	t.Helper()
	ix, err := index.Open(dir, index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open(%s): %v", dir, err)
	}
	s := New(Options{Index: ix})
	if _, err := s.Build(t.Context(), []search.Source{src}, func() bool { return true }, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}

// A built index leaves files behind. Without this the reopen test below could
// pass against an index that persists nothing and rebuilds silently.
func TestABuiltIndexIsOnDisk(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)
	dir := t.TempDir()
	buildOn(t, dir, src)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the index directory is empty, so nothing was persisted")
	}

	var bytes int64
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			t.Fatal(ierr)
		}
		bytes += info.Size()
	}
	if bytes == 0 {
		t.Errorf("the index wrote %d files totalling no bytes", len(entries))
	}
	t.Logf("the index persisted %d file(s), %d bytes", len(entries), bytes)
}

// Reopening the directory in a fresh instance answers what the first one did,
// which is what every restart depends on.
func TestAReopenedIndexAnswersTheSameQueries(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)
	dir := t.TempDir()
	first := buildOn(t, dir, src)

	queries := []string{"annual", "beach", "notes", "budget", "abcd", "海辺", "報告書"}
	before := map[string][]string{}
	for _, q := range queries {
		res, err := first.Query(t.Context(), []search.Source{src}, QueryOptions{Query: q, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if res.Tier != TierIndex {
			t.Fatalf("%q was not served from the index, so this proves nothing about persistence", q)
		}
		before[q] = hitPaths(res.Hits)
	}

	// A second instance over the same directory, with nothing carried across
	// but the files.
	reopened, err := index.Open(dir, index.DefaultConfig())
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	second := New(Options{Index: reopened})

	for _, q := range queries {
		res, qerr := second.Query(t.Context(), []search.Source{src}, QueryOptions{Query: q, Limit: 1000})
		if qerr != nil {
			t.Fatal(qerr)
		}
		if res.Tier != TierIndex {
			t.Errorf("%q fell back to %s after a reopen, so the persisted index was not usable", q, res.Tier)
			continue
		}
		if got := hitPaths(res.Hits); !slices.Equal(got, before[q]) {
			t.Errorf("%q answered differently after a reopen\n  before: %v\n  after:  %v", q, before[q], got)
		}
	}
}

// A reopened index still equals the walk. This is the equivalence property
// carried across the boundary the earlier test never crossed.
//
// "q1" is deliberately not in this list: two bytes form no trigram, so the
// index declines and the walk answers, before and after a reopen alike. That is
// the subject of its own test below rather than a hole here.
func TestAReopenedIndexStillEqualsTheWalk(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)
	dir := t.TempDir()
	buildOn(t, dir, src)

	reopened, err := index.Open(dir, index.DefaultConfig())
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	s := New(Options{Index: reopened})

	for _, q := range []string{"annual", "beach", "sunset", "notes", "budget", "abcd", "海辺"} {
		t.Run(q, func(t *testing.T) {
			indexed, walked := bothTiers(t, s, []search.Source{src}, q)
			if indexed.Tier != TierIndex {
				t.Fatalf("the reopened index did not serve %q", q)
			}
			if !slices.Equal(hitPaths(indexed.Hits), hitPaths(walked.Hits)) {
				t.Errorf("after a reopen the tiers disagree\n  index: %v\n  walk:  %v",
					hitPaths(indexed.Hits), hitPaths(walked.Hits))
			}
		})
	}
}

// A needle shorter than a trigram is answered by the walk rather than by a
// partial index guess, and the result is the same whether the index is fresh or
// reopened. Persistence does not change where that boundary sits.
//
// This was worth pinning because the reopen test above initially included such
// a query and read its fallback as a persistence failure. The fallback is the
// design: two bytes carry no trigram, so the index has nothing to look up.
func TestAShortNeedleWalksBeforeAndAfterAReopen(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)
	dir := t.TempDir()
	first := buildOn(t, dir, src)

	// One under the trigram floor and one at it, so the boundary is located
	// rather than just asserted on one side.
	for _, c := range []struct {
		query string
		tier  Tier
	}{
		{"q1", TierWalk},
		{"abc", TierIndex},
	} {
		t.Run(c.query, func(t *testing.T) {
			before, err := first.Query(t.Context(), []search.Source{src},
				QueryOptions{Query: c.query, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if before.Tier != c.tier {
				t.Errorf("a fresh index served %q from %s, want %s", c.query, before.Tier, c.tier)
			}

			reopened, oerr := index.Open(dir, index.DefaultConfig())
			if oerr != nil {
				t.Fatal(oerr)
			}
			after, err := New(Options{Index: reopened}).Query(t.Context(), []search.Source{src},
				QueryOptions{Query: c.query, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if after.Tier != before.Tier {
				t.Errorf("%q was served from %s before a reopen and %s after", c.query, before.Tier, after.Tier)
			}
			if !slices.Equal(hitPaths(before.Hits), hitPaths(after.Hits)) {
				t.Errorf("%q answered differently after a reopen\n  before: %v\n  after:  %v",
					c.query, hitPaths(before.Hits), hitPaths(after.Hits))
			}
			// And the short query still finds the file, which is what makes the
			// fallback a route rather than a refusal.
			if len(before.Hits) == 0 {
				t.Errorf("%q found nothing through either tier", c.query)
			}
		})
	}
}

// An index that exists but holds nothing must not answer.
//
// This is the state between an administrator enabling the index and the first
// build finishing: the directory is created, the index opens cleanly, it is
// nowhere near its entry ceiling, and it holds no entries. Answering from it
// means replying "no such file" about every file in the corpus, with a status
// reporting success and a tier reporting that the index served the query.
//
// It was found by wiring the services together the way a deployment does rather
// than the way each package's own fixture does, and it is inherited: the same
// probe against internal/search returned the same zero hits from the index tier.
func TestAnEmptyIndexDoesNotAnswer(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)

	// Opened, never built, which is what enabling the switch produces.
	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Index: ix})

	res, err := s.Query(t.Context(), []search.Source{src},
		QueryOptions{Query: "annual", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != TierWalk {
		t.Errorf("an empty index served the query from %s", res.Tier)
	}
	if len(res.Hits) == 0 {
		t.Error("the query found nothing, which is the defect: the files exist")
	}
	// The reason is surfaced rather than the result silently looking complete.
	if res.Fallback != index.FallbackIncomplete {
		t.Errorf("the fallback reason is %v, so nothing says why the index declined", res.Fallback)
	}

	// And once it is built, it answers.
	if _, berr := s.Build(t.Context(), []search.Source{src},
		func() bool { return true }, nil); berr != nil {
		t.Fatal(berr)
	}
	built, err := s.Query(t.Context(), []search.Source{src},
		QueryOptions{Query: "annual", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if built.Tier != TierIndex {
		t.Errorf("a built index still did not serve the query: %s", built.Tier)
	}
	if !slices.Equal(hitPaths(built.Hits), hitPaths(res.Hits)) {
		t.Errorf("the built index disagrees with the walk it replaced\n  walk:  %v\n  index: %v",
			hitPaths(res.Hits), hitPaths(built.Hits))
	}
}

// A file added after the build reaches the index through the updater, not
// through a rebuild, and it has to still be there after a restart.
//
// This is the delta path rather than the base segment: the build writes one
// segment, and every incremental change lands in a delta beside it. Dropping
// deltas on open leaves an index that answers correctly for everything indexed
// at build time and silently forgets everything indexed since, which is the
// worst shape a cache can take because nothing about it looks wrong.
func TestAnIncrementalUpdateSurvivesAReopen(t *testing.T) {
	src, dir := corpus(t, 1, equivalenceCorpus()...)
	idxDir := t.TempDir()
	s := buildOn(t, idxDir, src)

	// Added after the build, so it can only reach the index incrementally.
	writeFileForTest(t, dir, "reports/zephyr.txt")

	u := NewUpdater(s, func() []search.Source { return []search.Source{src} },
		slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	task.Go(ctx, "search index updater", func() { u.Run(ctx); close(done) })
	u.Offer(Change{Share: 1, Dir: "reports"})

	// Wait for the update to land rather than sleeping for a fixed time.
	var served bool
	for range 500 {
		res, err := s.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "zephyr", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if res.Tier == TierIndex && len(res.Hits) == 1 {
			served = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if !served {
		t.Fatal("the updater never indexed the new file, so the reopen below would prove nothing")
	}

	reopened, err := index.Open(idxDir, index.DefaultConfig())
	if err != nil {
		t.Fatalf("reopening after an incremental update: %v", err)
	}
	after, err := New(Options{Index: reopened}).Query(t.Context(), []search.Source{src},
		QueryOptions{Query: "zephyr", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if after.Tier != TierIndex {
		t.Fatalf("after a reopen the query fell back to %s", after.Tier)
	}
	if got := hitPaths(after.Hits); !slices.Equal(got, []string{"reports/zephyr.txt"}) {
		t.Errorf("the incrementally indexed file did not survive the reopen: %v", got)
	}
}

// A merge rewrites the segments underneath the index. Equivalence has to
// survive that too, or a deployment that has been running long enough to merge
// starts answering differently from one that has not.
func TestEquivalenceSurvivesAMerge(t *testing.T) {
	src, dir := corpus(t, 1, equivalenceCorpus()...)
	idxDir := t.TempDir()
	ix, err := index.Open(idxDir, index.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Index: ix})

	// Several builds, so there is more than one segment to merge. Each adds a
	// file so the passes are not identical.
	for i := range 3 {
		writeFileForTest(t, dir, "batch/file-"+string(rune('a'+i))+".txt")
		if _, berr := s.Build(t.Context(), []search.Source{src}, func() bool { return true }, nil); berr != nil {
			t.Fatalf("build %d: %v", i, berr)
		}
	}

	if merr := ix.Merge(t.Context(), func() bool { return true }); merr != nil {
		t.Fatalf("Merge: %v", merr)
	}

	for _, q := range []string{"annual", "beach", "notes", "abcd", "海辺", "file-a", "file-c"} {
		t.Run(q, func(t *testing.T) {
			indexed, walked := bothTiers(t, s, []search.Source{src}, q)
			if !slices.Equal(hitPaths(indexed.Hits), hitPaths(walked.Hits)) {
				t.Errorf("after a merge the tiers disagree for %q\n  index: %v\n  walk:  %v",
					q, hitPaths(indexed.Hits), hitPaths(walked.Hits))
			}
		})
	}
}

// A merged index survives a reopen, so the merge produced files a later process
// can read rather than state that lived only in the merging process.
func TestAMergedIndexReopens(t *testing.T) {
	src, dir := corpus(t, 1, equivalenceCorpus()...)
	idxDir := t.TempDir()
	ix, err := index.Open(idxDir, index.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Index: ix})
	for i := range 3 {
		writeFileForTest(t, dir, "batch/file-"+string(rune('a'+i))+".txt")
		if _, berr := s.Build(t.Context(), []search.Source{src}, func() bool { return true }, nil); berr != nil {
			t.Fatal(berr)
		}
	}
	if merr := ix.Merge(t.Context(), func() bool { return true }); merr != nil {
		t.Fatalf("Merge: %v", merr)
	}

	reopened, err := index.Open(idxDir, index.DefaultConfig())
	if err != nil {
		t.Fatalf("reopening a merged index: %v", err)
	}
	after := New(Options{Index: reopened})

	res, err := after.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "beach", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != TierIndex {
		t.Fatalf("a merged index did not serve its own query after a reopen: %s", res.Tier)
	}
	if len(res.Hits) == 0 {
		t.Error("a merged and reopened index found nothing")
	}
}

// The design says the index directory can be deleted with no data loss, because
// it is a cache. That is a claim about what happens next, so it is worth
// running: with the directory gone, search still answers, from the walk.
func TestDeletingTheIndexDirectoryLosesNothing(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)
	idxDir := t.TempDir()
	s := buildOn(t, idxDir, src)

	fromIndex, err := s.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "beach", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if fromIndex.Tier != TierIndex {
		t.Fatal("the query was not served from the index to begin with")
	}

	if rerr := os.RemoveAll(idxDir); rerr != nil {
		t.Fatal(rerr)
	}
	// A fresh service with no index at all is what the next start would build
	// after finding the directory gone.
	plain := New(Options{})
	fromWalk, err := plain.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "beach", Limit: 100})
	if err != nil {
		t.Fatalf("search stopped working once the cache was deleted: %v", err)
	}
	if !slices.Equal(hitPaths(fromIndex.Hits), hitPaths(fromWalk.Hits)) {
		t.Errorf("deleting the cache changed the answer\n  before: %v\n  after:  %v",
			hitPaths(fromIndex.Hits), hitPaths(fromWalk.Hits))
	}
}

// A truncated segment file is a torn write, which is what a power loss during a
// merge leaves behind. The index is a cache, so the requirement is that search
// keeps answering: a refusal to open, or a fallback, are both acceptable, and
// returning wrong results or crashing are not.
func TestATruncatedIndexDoesNotBreakSearch(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)
	idxDir := t.TempDir()
	buildOn(t, idxDir, src)

	entries, err := os.ReadDir(idxDir)
	if err != nil {
		t.Fatal(err)
	}
	truncated := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(idxDir, e.Name())
		info, ierr := e.Info()
		if ierr != nil || info.Size() < 2 {
			continue
		}
		// Half the file, which is a tear rather than an empty file.
		if terr := os.Truncate(p, info.Size()/2); terr != nil {
			t.Fatal(terr)
		}
		truncated++
	}
	if truncated == 0 {
		t.Skip("the index wrote no file large enough to tear")
	}

	// Opening may refuse, which is a valid answer for a cache that cannot be
	// trusted. What must not happen is a panic or a wrong result.
	var s *Service
	if ix, oerr := index.Open(idxDir, index.DefaultConfig()); oerr == nil {
		s = New(Options{Index: ix})
	} else {
		t.Logf("the torn index refused to open, which is a valid answer: %v", oerr)
		s = New(Options{})
	}

	res, err := s.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "beach", Limit: 100})
	if err != nil {
		t.Fatalf("a torn index stopped search answering: %v", err)
	}
	// Whatever it returns has to be right. A partial index may legitimately
	// return nothing and fall back; it may not invent a path.
	for _, h := range res.Hits {
		if !slices.Contains(equivalenceCorpus(), h.Path) {
			t.Errorf("a torn index returned a path that is not in the corpus: %q", h.Path)
		}
	}
	t.Logf("after truncating %d file(s) the %s tier answered with %d hit(s)",
		truncated, res.Tier, len(res.Hits))
}

// The state a deployment reaches the moment the index is enabled, through
// OpenIndex rather than by constructing an index directly.
//
// OpenIndex reports OpenAbsent for a directory nothing was built into, and a
// caller may reasonably attach the index anyway: the state is advisory and the
// handle is usable. What must not happen is that attaching it makes search
// answer from nothing, which is why the emptiness check lives in Query rather
// than in whoever decides to attach.
func TestAFreshlyOpenedIndexDoesNotAnswer(t *testing.T) {
	src, _ := corpus(t, 1, equivalenceCorpus()...)
	dir := filepath.Join(t.TempDir(), "index")

	ix, st := OpenIndex(dir, index.DefaultConfig(), nil)
	if st != OpenAbsent {
		t.Fatalf("a directory nothing built into reported %v, want absent", st)
	}
	if ix == nil {
		t.Fatal("OpenIndex returned no index for a fresh directory, so this proves nothing")
	}

	// Attached despite the absent state, which is the case that matters.
	s := New(Options{Index: ix})
	res, err := s.Query(t.Context(), []search.Source{src},
		QueryOptions{Query: "annual", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != TierWalk {
		t.Errorf("a freshly opened index served the query from %s", res.Tier)
	}
	if len(res.Hits) == 0 {
		t.Error("search found nothing for a file that exists")
	}
}
