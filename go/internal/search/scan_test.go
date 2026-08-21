package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Measuring a corpus, checked against trees whose contents are known.
//
// The point of the scan is that an administrator can size a disk before
// building an index, so what matters is that the counts are the real ones and
// that a scan which ran out says so instead of reporting its sample as the
// whole.

func scanRoot(t *testing.T, names []string) []Source {
	t.Helper()

	dir := t.TempDir()
	for _, n := range names {
		full := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	root, err := vfs.OpenShareRoot(1, dir, vfs.DefaultSharePolicy())
	if err != nil {
		t.Fatalf("opening the share: %v", err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("closing the share: %v", cerr)
		}
	})
	return []Source{{Root: root, Base: vfs.RootPath()}}
}

func TestAScanCountsEveryFileAndTheirNameBytes(t *testing.T) {
	sources := scanRoot(t, []string{
		"report.txt",
		"photos/holiday.jpg",
		"photos/nested/deep.png",
	})

	got, err := ScanCorpus(context.Background(), sources, ScanOptions{})
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if got.Stats.Files != 3 {
		t.Errorf("counted %d files, want 3", got.Stats.Files)
	}
	if got.Partial {
		t.Error("a complete scan reported itself as a sample")
	}
	if got.Stats.NameBytesTotal == 0 {
		t.Error("no name bytes were measured, so the estimate has nothing to size against")
	}
	// The sketch is the term that separates one script's corpus from another
	// at the same file count, so an estimate without it is the wrong estimate.
	if got.Stats.DistinctTrigramsEst == 0 {
		t.Error("no distinct trigrams were counted")
	}
}

// A directory is traversed but is not corpus: the index holds files, so
// counting directory names would size it against entries no query returns.
func TestDirectoryNamesAreNotCounted(t *testing.T) {
	flat := scanRoot(t, []string{"a.txt", "b.txt"})
	nested := scanRoot(t, []string{"one/two/three/a.txt", "one/two/three/b.txt"})

	first, err := ScanCorpus(context.Background(), flat, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, serr := ScanCorpus(context.Background(), nested, ScanOptions{})
	if serr != nil {
		t.Fatal(serr)
	}

	if first.Stats.Files != second.Stats.Files {
		t.Errorf("the same files counted as %d and %d depending on depth", first.Stats.Files, second.Stats.Files)
	}
	if first.Stats.NameBytesTotal != second.Stats.NameBytesTotal {
		t.Errorf("directory names reached the measurement: %d against %d",
			first.Stats.NameBytesTotal, second.Stats.NameBytesTotal)
	}
	if second.DirsVisited <= first.DirsVisited {
		t.Error("the nested tree was not actually traversed, so this proves nothing")
	}
}

// The bound is what stops the scan, and reaching it is reported rather than
// refused: a partial measurement is a real sample of a real corpus.
func TestTheEntryBoundStopsTheScanAndSaysSo(t *testing.T) {
	var names []string
	for i := range 40 {
		names = append(names, fmt.Sprintf("file-%02d.txt", i))
	}
	sources := scanRoot(t, names)

	got, err := ScanCorpus(context.Background(), sources, ScanOptions{MaxEntries: 10})
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if !got.Partial {
		t.Fatal("the scan ran past its bound without saying so, so a sample would be read as the whole corpus")
	}
	if got.Stats.Files != 10 {
		t.Errorf("counted %d files, want the bound to be what stopped it", got.Stats.Files)
	}

	// Without the bound the same tree measures whole, which is what proves the
	// bound is what refused rather than the tree running out.
	full, ferr := ScanCorpus(context.Background(), sources, ScanOptions{})
	if ferr != nil {
		t.Fatal(ferr)
	}
	if full.Partial || full.Stats.Files != 40 {
		t.Fatalf("the unbounded scan counted %d files (partial=%v), want all 40", full.Stats.Files, full.Partial)
	}
}

// The compiled-in bound is the default, so a caller that names no bound still
// has one.
func TestTheDefaultBoundIsTheCompiledInOne(t *testing.T) {
	if limits.CorpusScanEntries <= 0 {
		t.Fatal("the compiled-in bound is not a bound")
	}
	sources := scanRoot(t, []string{"a.txt"})
	// Nothing here reaches the bound; what is checked is that asking for none
	// does not mean none.
	got, err := ScanCorpus(context.Background(), sources, ScanOptions{MaxEntries: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got.Partial {
		t.Error("a one-file tree was reported as a sample")
	}
}

// A cancelled scan stops. It runs against a live filesystem on an
// administrator's request, and one that ignores cancellation holds the
// resources it was told to release.
func TestACancelledScanStops(t *testing.T) {
	sources := scanRoot(t, []string{"a.txt", "b/c.txt"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ScanCorpus(ctx, sources, ScanOptions{}); err == nil {
		t.Fatal("a cancelled scan ran to completion")
	}
}

// An entry the caller may not see is not measured, so an estimate does not
// leak the size of what they cannot search.
func TestAnEntryTheCallerCannotSeeIsNotMeasured(t *testing.T) {
	sources := scanRoot(t, []string{"visible.txt", "secret.txt"})
	sources[0].Allow = func(p vfs.SafePath, _ bool) bool {
		return p.Name() != "secret.txt"
	}

	got, err := ScanCorpus(context.Background(), sources, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Files != 1 {
		t.Fatalf("counted %d files, want only the visible one", got.Stats.Files)
	}
}

// The measurement feeds the estimator, which is the only reason it exists.
func TestAMeasuredCorpusSizesAnIndex(t *testing.T) {
	sources := scanRoot(t, []string{"report.txt", "photos/holiday.jpg"})

	got, err := ScanCorpus(context.Background(), sources, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	est := EstimateNameIndex(got.Stats, 32)
	if est.IndexBytes == 0 {
		t.Fatal("a corpus with files in it estimated to nothing")
	}
	if est.Formula == "" {
		t.Error("no derivation, so an estimate that turns out wrong cannot be checked term by term")
	}
}
