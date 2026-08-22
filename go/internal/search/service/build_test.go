package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Building the index, checked by querying it afterwards.
//
// What matters is not that the build reported a file count: it is that a query
// which would have walked now answers from the index. A test that only counted
// entries would pass against a build that wrote them somewhere nothing reads.

func buildFixture(t *testing.T, names []string) (*Service, []search.Source) {
	t.Helper()
	return buildFixtureIn(t, t.TempDir(), names)
}

// buildFixtureIn is the same with the directory named, for a test that also
// has to reach the share on disk.
func buildFixtureIn(t *testing.T, dir string, names []string) (*Service, []search.Source) {
	t.Helper()

	share := filepath.Join(dir, "share")
	for _, n := range names {
		full := filepath.Join(share, n)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	root, err := vfs.OpenShareRoot(1, share, vfs.DefaultSharePolicy())
	if err != nil {
		t.Fatalf("opening the share: %v", err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("closing the share: %v", cerr)
		}
	})

	ix, ierr := index.Open(filepath.Join(dir, "index"), index.DefaultConfig())
	if ierr != nil {
		t.Fatalf("opening the index: %v", ierr)
	}
	svc := New(Options{Clock: clock.System(), Storage: StorageSSD, CPUs: 2, Index: ix})

	return svc, []search.Source{{Share: 1, Root: root, Base: vfs.RootPath()}}
}

func TestABuildIndexesWhatIsOnDiskAndAQueryFindsIt(t *testing.T) {
	svc, sources := buildFixture(t, []string{
		"report.txt",
		"photos/holiday.jpg",
		"photos/nested/summary.txt",
	})

	progress, err := svc.Build(context.Background(), sources, nil, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if progress.Files != 3 {
		t.Fatalf("indexed %d files, want 3", progress.Files)
	}
	if progress.Partial {
		t.Error("a three-file corpus reported as partial")
	}

	// The query is the check. It has to come back from the index rather than
	// falling back, which is the whole reason the build exists.
	got, qerr := svc.Query(context.Background(), sources, QueryOptions{Query: "holiday", Limit: 10})
	if qerr != nil {
		t.Fatalf("querying: %v", qerr)
	}
	if len(got.Hits) != 1 {
		t.Fatalf("got %d hits, want the one file named that", len(got.Hits))
	}
	if got.Hits[0].Name != "holiday.jpg" {
		t.Errorf("the hit is %q", got.Hits[0].Name)
	}
}

// A directory is walked but not indexed: the index holds files, and returning
// a directory from a name query would be a hit nothing can open.
func TestADirectoryIsWalkedButNotIndexed(t *testing.T) {
	svc, sources := buildFixture(t, []string{"holiday/a.txt"})

	progress, err := svc.Build(context.Background(), sources, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Files != 1 {
		t.Fatalf("indexed %d entries, want only the file", progress.Files)
	}
	if progress.Dirs < 2 {
		t.Errorf("visited %d directories, so the nested one was not walked", progress.Dirs)
	}
}

// The gate is what a cancellation reaches. A build that ignores it walks every
// share regardless, which is the thing that makes search something an
// administrator turns off.
func TestTheGateStopsABuildAndKeepsWhatItWrote(t *testing.T) {
	var names []string
	for i := range 200 {
		names = append(names, fmt.Sprintf("dir-%03d/file.txt", i))
	}
	svc, sources := buildFixture(t, names)

	calls := 0
	gate := func() bool {
		calls++
		// Stop after a few directories, with plenty left.
		return calls < 5
	}

	progress, err := svc.Build(context.Background(), sources, gate, nil)
	if err != nil {
		t.Fatalf("a stopped build reported an error: %v", err)
	}
	if progress.Dirs >= 200 {
		t.Fatalf("the gate did not stop the build: %d directories walked", progress.Dirs)
	}
	// What it wrote stays. The index is allowed to hold less than the corpus,
	// because a query that misses falls back to a walk.
	if progress.Files == 0 {
		t.Error("a stopped build discarded everything it had already indexed")
	}
}

// A cancelled build stops. It walks every share, so one that ignores
// cancellation holds the disk for as long as the corpus takes.
func TestACancelledBuildStops(t *testing.T) {
	svc, sources := buildFixture(t, []string{"a.txt", "b/c.txt"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.Build(ctx, sources, nil, nil); err == nil {
		t.Fatal("a cancelled build ran to completion")
	}
}

// Building without an index open is refused rather than silently doing
// nothing, which would look like a build that worked.
func TestBuildingWithNoIndexIsRefused(t *testing.T) {
	svc := New(Options{Clock: clock.System(), Storage: StorageSSD, CPUs: 2})

	if _, err := svc.Build(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("a build with no index open reported success")
	}
}

// Progress is reported as the build runs, so a long one is visible rather than
// a request that has not answered yet.
func TestProgressIsReportedWhileTheBuildRuns(t *testing.T) {
	var names []string
	for i := range buildBatch + 10 {
		names = append(names, fmt.Sprintf("f%05d.txt", i))
	}
	svc, sources := buildFixture(t, names)

	var seen []int64
	if _, err := svc.Build(context.Background(), sources, nil, func(p BuildProgress) {
		seen = append(seen, p.Files)
	}); err != nil {
		t.Fatal(err)
	}

	if len(seen) < 2 {
		t.Fatalf("progress was reported %d times for a corpus larger than one batch", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("progress went backwards: %v", seen)
		}
	}
}
