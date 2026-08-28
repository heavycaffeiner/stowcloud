//go:build linux

package search

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// corpus builds a share root holding the named files, creating parent
// directories as it goes. A path ending in a slash is a directory.
func corpus(t *testing.T, share uint32, names ...string) Source {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		full := filepath.Join(dir, filepath.FromSlash(n))
		if n != "" && n[len(n)-1] == '/' {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", n, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", n, err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}

	root, _, err := vfs.RegisterShareRoot(vfs.ShareID(share), dir, vfs.DefaultSharePolicy())
	if err != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return Source{Share: share, Root: root, Base: vfs.RootPath()}
}

func paths(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Path)
	}
	return out
}

func has(hits []Hit, path string) bool {
	for _, h := range hits {
		if h.Path == path {
			return true
		}
	}
	return false
}

func TestWalkFindsMatchesAcrossASubtree(t *testing.T) {
	src := corpus(t, 1, "report.pdf", "a/report-2024.pdf", "a/b/notes.txt", "other.bin")

	res, err := Walk(t.Context(), []Source{src}, WalkOptions{Needle: FoldString("report")})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("got %d hits (%v), want 2", len(res.Hits), paths(res.Hits))
	}
	if !has(res.Hits, "report.pdf") || !has(res.Hits, "a/report-2024.pdf") {
		t.Errorf("missing an expected hit: %v", paths(res.Hits))
	}
	// Neither name is an exact match for "report", so both take the prefix
	// weight alone and the tie breaks on path, which is what makes the order
	// reproducible rather than dependent on which worker got there first.
	if got := paths(res.Hits); got[0] != "a/report-2024.pdf" || got[1] != "report.pdf" {
		t.Errorf("equal scores did not break on path: %v", got)
	}
	if res.DirsVisited < 3 {
		t.Errorf("visited %d directories, want at least 3", res.DirsVisited)
	}
}

// An empty needle matches everything, which is how a scoped listing is
// expressed.
func TestWalkWithNoNeedleReturnsEverything(t *testing.T) {
	src := corpus(t, 1, "a.txt", "sub/b.txt", "sub/")

	res, err := Walk(t.Context(), []Source{src}, WalkOptions{})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// Two files and the directory itself.
	if len(res.Hits) != 3 {
		t.Errorf("got %d hits (%v), want 3", len(res.Hits), paths(res.Hits))
	}
}

// The permission check runs before an entry is scored: search sweeps the whole
// tree, so it is the broadest place an existence leak could open.
func TestWalkAppliesAllowBeforeScoring(t *testing.T) {
	src := corpus(t, 1, "public/report.pdf", "private/report.pdf")
	src.Allow = func(p vfs.SafePath, _ bool) bool {
		s := p.String()
		return s != "private" && s != "private/report.pdf"
	}

	res, err := Walk(t.Context(), []Source{src}, WalkOptions{Needle: FoldString("report")})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Path != "public/report.pdf" {
		t.Errorf("the refused subtree leaked: %v", paths(res.Hits))
	}
}

// A nil Allow is the administrator-scoped form and the walker skips the call
// entirely, so the administrator path pays no per-entry closure cost.
func TestWalkWithNilAllowSeesEverything(t *testing.T) {
	src := corpus(t, 1, "a/report.pdf", "b/report.pdf")
	if src.Allow != nil {
		t.Fatal("the fixture should carry no closure")
	}
	res, err := Walk(t.Context(), []Source{src}, WalkOptions{Needle: FoldString("report")})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Errorf("got %v, want both", paths(res.Hits))
	}
}

// The walker calls Allow from several goroutines at once with no
// synchronisation of its own, which is the contract a closure has to be safe
// for. Run under -race this is the test that proves it.
func TestWalkCallsAllowConcurrently(t *testing.T) {
	names := make([]string, 0, 64)
	for i := range 32 {
		names = append(names,
			filepath.Join("d"+string(rune('a'+i%8)), "f", "report", string(rune('a'+i))+".txt"))
	}
	src := corpus(t, 1, names...)

	var calls atomic.Int64
	src.Allow = func(vfs.SafePath, bool) bool {
		calls.Add(1)
		return true
	}

	res, err := Walk(t.Context(), []Source{src}, WalkOptions{Needle: FoldString(".txt"), Threads: 8})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if calls.Load() == 0 {
		t.Error("Allow was never called")
	}
	if len(res.Hits) == 0 {
		t.Error("the walk found nothing")
	}
}

// A truncated result says so rather than presenting a partial answer as a
// complete one.
func TestWalkReportsTruncation(t *testing.T) {
	names := make([]string, 0, 10)
	for i := range 10 {
		names = append(names, "report-"+string(rune('a'+i))+".txt")
	}
	src := corpus(t, 1, names...)

	res, err := Walk(t.Context(), []Source{src}, WalkOptions{Needle: FoldString("report"), Limit: 3})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(res.Hits) != 3 || !res.Truncated {
		t.Errorf("got %d hits, truncated %v; want 3 and true", len(res.Hits), res.Truncated)
	}
}

// The stat phase is the only thing that fills size and time; a name-only query
// leaves them nil rather than guessing.
func TestWalkMetadataIsOptional(t *testing.T) {
	src := corpus(t, 1, "report.pdf")

	bare, err := Walk(t.Context(), []Source{src}, WalkOptions{Needle: FoldString("report")})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if bare.Hits[0].Size != nil || bare.Hits[0].MTimeNs != nil {
		t.Error("a name-only walk resolved metadata nobody asked for")
	}

	full, err := Walk(t.Context(), []Source{src},
		WalkOptions{Needle: FoldString("report"), WithMetadata: true})
	if err != nil {
		t.Fatalf("walk with metadata: %v", err)
	}
	if full.Hits[0].Size == nil || full.Hits[0].MTimeNs == nil {
		t.Error("the stat phase left metadata unresolved")
	}
}

// A cancelled walk stops rather than finishing the tree.
func TestWalkStopsOnCancellation(t *testing.T) {
	src := corpus(t, 1, "a/b/c/d.txt")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := Walk(ctx, []Source{src}, WalkOptions{}); err == nil {
		t.Error("a cancelled walk returned no error")
	}
}

// A broken share's nil root is skipped rather than panicking: the core passes
// one through by contract and the walker owns the skip.
func TestWalkSkipsASourceWithNoRoot(t *testing.T) {
	good := corpus(t, 1, "report.pdf")
	broken := Source{Share: 2, Base: vfs.RootPath()}

	res, err := Walk(t.Context(), []Source{broken, good}, WalkOptions{Needle: FoldString("report")})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Errorf("got %v, want the one reachable hit", paths(res.Hits))
	}
}

// Prefix is this package's own field for rendering a hit path, so a hit names
// what the caller asked about rather than a share-relative fragment.
func TestWalkAppliesThePrefixToReportedPaths(t *testing.T) {
	src := corpus(t, 1, "report.pdf")
	src.Prefix = "shared/"

	res, err := Walk(t.Context(), []Source{src}, WalkOptions{Needle: FoldString("report")})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if res.Hits[0].Path != "shared/report.pdf" {
		t.Errorf("got %q, want the prefixed path", res.Hits[0].Path)
	}
}

// The two walks agree on membership: they differ in cost by design, not in
// which names they find.
func TestScanCorpusAndWalkAgreeOnMembership(t *testing.T) {
	src := corpus(t, 1, "a.txt", "d/b.txt", "d/e/c.txt")

	walk, err := Walk(t.Context(), []Source{src}, WalkOptions{})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var files uint64
	for _, h := range walk.Hits {
		if !h.IsDir {
			files++
		}
	}

	scan, err := ScanCorpus(t.Context(), []Source{src}, ScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := scan.Stats.Files; got != uint64(files) {
		t.Errorf("the scan counted %d files and the walk found %d", got, files)
	}
	if scan.Stats.DistinctTrigramsEst == 0 {
		t.Error("the scan measured no trigrams")
	}
}

// The same Allow closure filters the same paths through both walks, which is
// the adapter's obligation stated as a test.
func TestBothWalksApplyTheSameAllow(t *testing.T) {
	build := func() Source {
		s := corpus(t, 1, "public/a.txt", "private/b.txt")
		s.Allow = func(p vfs.SafePath, _ bool) bool {
			s := p.String()
			return s != "private" && s != "private/b.txt"
		}
		return s
	}

	walk, err := Walk(t.Context(), []Source{build()}, WalkOptions{})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, h := range walk.Hits {
		if h.Path == "private/b.txt" || h.Path == "private" {
			t.Errorf("the walk returned a refused path: %v", paths(walk.Hits))
		}
	}

	scan, err := ScanCorpus(t.Context(), []Source{build()}, ScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Stats.Files != 1 {
		t.Errorf("the scan counted %d files, want the one visible file", scan.Stats.Files)
	}
}

// A scan that ran out measured a real sample and says so, rather than
// reporting the fraction it saw as the whole.
func TestScanCorpusReportsAPartialMeasurement(t *testing.T) {
	src := corpus(t, 1, "a.txt", "b.txt", "c.txt", "d.txt")

	res, err := ScanCorpus(t.Context(), []Source{src}, ScanOptions{MaxEntries: 2})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Partial {
		t.Error("a bounded scan did not report itself partial")
	}
	if res.Stats.Files != 2 {
		t.Errorf("counted %d files, want the bound of 2", res.Stats.Files)
	}
}

// The adapter is field for field, and it drops a broken share's nil root
// because the walker cannot open one.
func TestSourcesOfAdaptsEveryFieldAndDropsABrokenShare(t *testing.T) {
	src := corpus(t, 7, "a.txt")
	base, err := vfs.RootPath().JoinExisting("a.txt")
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	var called bool
	allow := func(vfs.SafePath, bool) bool { called = true; return true }

	got := SourcesOf([]core.ScanSource{
		{Share: 7, Root: src.Root, Base: base, Allow: allow},
		{Share: 8, Root: nil, Base: vfs.RootPath()},
	})
	if len(got) != 1 {
		t.Fatalf("got %d sources, want the one with a root", len(got))
	}
	if got[0].Share != 7 {
		t.Errorf("share came across as %d, want 7", got[0].Share)
	}
	if got[0].Root != src.Root {
		t.Error("root did not come across")
	}
	if got[0].Base.String() != base.String() {
		t.Errorf("base came across as %q, want %q", got[0].Base.String(), base.String())
	}
	if got[0].Prefix != "" {
		t.Errorf("the adapter invented a prefix: %q", got[0].Prefix)
	}
	// The adapter must not call the closure itself.
	if called {
		t.Error("the adapter called Allow")
	}
	if got[0].Allow == nil {
		t.Fatal("Allow did not come across")
	}
	got[0].Allow(vfs.RootPath(), false)
	if !called {
		t.Error("the carried closure is not the one that was handed in")
	}
}

// A nil Allow stays nil rather than becoming a closure that returns true: the
// walker skips the call entirely for the administrator-scoped form.
func TestSourcesOfKeepsANilAllowNil(t *testing.T) {
	src := corpus(t, 1, "a.txt")
	got := SourcesOf([]core.ScanSource{{Share: 1, Root: src.Root, Base: vfs.RootPath()}})
	if len(got) != 1 || got[0].Allow != nil {
		t.Error("a nil Allow did not stay nil")
	}
}
