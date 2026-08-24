//go:build linux

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

func newRoot(t *testing.T, files []string) (*vfs.ShareRoot, string) {
	t.Helper()
	host := t.TempDir()
	for _, rel := range files {
		full := filepath.Join(host, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
	root, err := vfs.OpenShareRoot(1, host, vfs.DefaultSharePolicy())
	if err != nil {
		t.Fatalf("OpenShareRoot: %v", err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("closing the share root: %v", cerr)
		}
	})
	return root, host
}

func walkFor(t *testing.T, root *vfs.ShareRoot, needle string, opts ...func(*WalkOptions)) WalkResult {
	t.Helper()
	o := WalkOptions{Needle: FoldString(needle), Threads: 4}
	for _, f := range opts {
		f(&o)
	}
	res, err := Walk(context.Background(), []Source{{Share: 1, Root: root}}, o)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return res
}

func names(r WalkResult) []string {
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, h.Path)
	}
	return out
}

func TestTheWalkFindsNamesAtEveryDepth(t *testing.T) {
	root, _ := newRoot(t, []string{
		"report.txt",
		"a/report.txt",
		"a/b/c/report.txt",
		"a/b/c/other.txt",
	})
	got := names(walkFor(t, root, "report"))
	if len(got) != 3 {
		t.Fatalf("got %v, want three reports", got)
	}
}

func TestTheWalkIsCaseInsensitive(t *testing.T) {
	root, _ := newRoot(t, []string{"IMG_0001.JPG", "notes.txt"})
	got := names(walkFor(t, root, "img_0001"))
	if len(got) != 1 {
		t.Fatalf("got %v, want the mixed-case name", got)
	}
}

// A name-only query must never stat. Published measurement puts metadata at
// roughly half the cost of a walk, so statting for information nobody asked
// for is double price.
func TestANameOnlyQueryDoesNotResolveMetadata(t *testing.T) {
	root, _ := newRoot(t, []string{"report.txt"})
	res := walkFor(t, root, "report")
	if len(res.Hits) != 1 {
		t.Fatalf("got %v", names(res))
	}
	if res.Hits[0].Size != nil || res.Hits[0].MTimeNs != nil {
		t.Fatalf("a name-only query resolved metadata: %+v", res.Hits[0])
	}
}

func TestTheStatPhaseResolvesMetadataWhenAsked(t *testing.T) {
	root, _ := newRoot(t, []string{"report.txt"})
	res := walkFor(t, root, "report", func(o *WalkOptions) { o.WithMetadata = true })
	if len(res.Hits) != 1 {
		t.Fatalf("got %v", names(res))
	}
	if res.Hits[0].Size == nil || res.Hits[0].MTimeNs == nil {
		t.Fatalf("the stat phase resolved nothing: %+v", res.Hits[0])
	}
}

// The permission check runs before an entry is scored. Search sweeps the whole
// tree, so it is the broadest place an existence leak could open.
func TestAnEntryTheCallerCannotSeeIsNeverReported(t *testing.T) {
	root, _ := newRoot(t, []string{"visible/report.txt", "secret/report.txt"})

	res, err := Walk(context.Background(), []Source{{
		Share: 1, Root: root,
		Allow: func(p vfs.SafePath, _ bool) bool {
			return !strings.HasPrefix(p.String(), "secret")
		},
	}}, WalkOptions{Needle: FoldString("report"), Threads: 2})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	got := names(res)
	if len(got) != 1 || !strings.HasPrefix(got[0], "visible") {
		t.Fatalf("got %v, want only the visible report", got)
	}
}

// A refused subtree must not be descended into, or the walk pays for a
// directory the caller cannot see.
func TestARefusedSubtreeIsNotWalked(t *testing.T) {
	var files []string
	for i := 0; i < 50; i++ {
		files = append(files, fmt.Sprintf("secret/deep/f%02d.txt", i))
	}
	files = append(files, "visible/report.txt")
	root, _ := newRoot(t, files)

	res, err := Walk(context.Background(), []Source{{
		Share: 1, Root: root,
		Allow: func(p vfs.SafePath, _ bool) bool {
			return !strings.HasPrefix(p.String(), "secret")
		},
	}}, WalkOptions{Needle: FoldString("report"), Threads: 1})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	// The share root and the visible directory, and nothing under secret.
	if res.DirsVisited > 3 {
		t.Fatalf("visited %d directories, so a refused subtree was walked", res.DirsVisited)
	}
}

// The timing claim, which the brief makes a test rather than an aspiration: a
// query matching many entries the caller cannot see must not be measurably
// slower than one matching none.
func TestAQueryOverInvisibleEntriesIsNotSlower(t *testing.T) {
	var files []string
	for i := 0; i < 400; i++ {
		files = append(files, fmt.Sprintf("secret/report%03d.txt", i))
	}
	root, _ := newRoot(t, files)

	deny := func(vfs.SafePath, bool) bool { return false }
	// testing.Benchmark rather than a wall-clock subtraction: it uses the
	// monotonic timer, which is what a duration wants, and D8 keeps time.Now
	// inside internal/clock.
	run := func(needle string) time.Duration {
		res := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := Walk(context.Background(),
					[]Source{{Share: 1, Root: root, Allow: deny}},
					WalkOptions{Needle: FoldString(needle), Threads: 1}); err != nil {
					b.Fatalf("Walk: %v", err)
				}
			}
		})
		if res.N == 0 {
			t.Fatal("the walk never ran")
		}
		return time.Duration(res.NsPerOp())
	}

	// "report" would match all four hundred; "zzzznomatch" matches none. With
	// the check before the scoring, both do the same work.
	matching := run("report")
	notMatching := run("zzzznomatch")

	ratio := float64(matching) / float64(notMatching)
	if ratio > 3 || ratio < 1.0/3 {
		t.Fatalf("a query matching invisible entries took %v against %v for one matching none "+
			"(ratio %.2f): the ACL check is not running before the scoring",
			matching, notMatching, ratio)
	}
}

// The result set is capped, and a truncated result says so rather than looking
// complete.
func TestATruncatedResultSaysSo(t *testing.T) {
	var files []string
	for i := 0; i < 40; i++ {
		files = append(files, fmt.Sprintf("report%02d.txt", i))
	}
	root, _ := newRoot(t, files)

	res := walkFor(t, root, "report", func(o *WalkOptions) { o.Limit = 10 })
	if len(res.Hits) != 10 {
		t.Fatalf("got %d hits, want the limit of 10", len(res.Hits))
	}
	if !res.Truncated {
		t.Fatal("a truncated result did not say so, so it looks complete")
	}

	full := walkFor(t, root, "report", func(o *WalkOptions) { o.Limit = 100 })
	if full.Truncated {
		t.Fatal("a complete result claimed to be truncated")
	}
}

// A search the client abandoned has to stop rather than walk to the end.
func TestACancelledWalkStops(t *testing.T) {
	var files []string
	for i := 0; i < 200; i++ {
		files = append(files, fmt.Sprintf("d%02d/f%02d.txt", i%20, i))
	}
	root, _ := newRoot(t, files)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Walk(ctx, []Source{{Share: 1, Root: root}},
		WalkOptions{Needle: FoldString("f"), Threads: 4})
	if err == nil {
		t.Fatal("a cancelled walk returned a result")
	}
}

// Reserved control names are this server's own bookkeeping, not documents.
func TestReservedNamesAreNotSearchable(t *testing.T) {
	root, host := newRoot(t, []string{"report.txt"})
	part := filepath.Join(host, ".scpart-report-in-progress")
	if err := os.WriteFile(part, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing the part file: %v", err)
	}
	got := names(walkFor(t, root, "report"))
	if len(got) != 1 || strings.Contains(got[0], "scpart") {
		t.Fatalf("got %v, want the part file hidden", got)
	}
}

// An unreadable directory must not lose every other hit.
func TestAnUnreadableDirectoryDoesNotFailTheWalk(t *testing.T) {
	root, host := newRoot(t, []string{"ok/report.txt", "blocked/report.txt"})
	if err := os.Chmod(filepath.Join(host, "blocked"), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() {
		// Restore the owner bits so TempDir cleanup can remove it. Nothing
		// else needs to reach it, so this is narrower than it was.
		//nolint:errcheck,gosec // G302 reads this as a file: it is a directory, which needs its execute bit back for cleanup to descend.
		_ = os.Chmod(filepath.Join(host, "blocked"), 0o700)
	})

	got := names(walkFor(t, root, "report"))
	if len(got) < 1 {
		t.Fatalf("got %v, want the readable hit to survive", got)
	}
}

// Hits come back best first, and ties break on the path so two runs agree.
func TestWalkHitsAreOrdered(t *testing.T) {
	root, _ := newRoot(t, []string{
		"zzz/report.txt",
		"aaa/report.txt",
		"report",
		"my_report_final.txt",
	})
	res := walkFor(t, root, "report")
	if len(res.Hits) < 3 {
		t.Fatalf("got %v", names(res))
	}
	if res.Hits[0].Name != "report" {
		t.Fatalf("the exact match is not first: %v", names(res))
	}
	for i := 1; i < len(res.Hits); i++ {
		if res.Hits[i-1].Score < res.Hits[i].Score {
			t.Fatalf("hits are not ordered by score: %+v", res.Hits)
		}
	}
}

// The stat batch is ordered by device and inode, because filesystems lay
// inodes out in increasing order and asking that way makes the disk seek
// forward only.
func TestTheStatBatchIsInodeOrdered(t *testing.T) {
	p := []pending{
		{dev: 1, ino: 500, hasIno: true},
		{dev: 1, ino: 100, hasIno: true},
		{dev: 0, ino: 900, hasIno: true},
		{dev: 1, hasIno: false, dirSeq: 2},
		{dev: 1, hasIno: false, dirSeq: 1},
	}
	sortForStat(p)

	if p[0].dev != 0 {
		t.Fatalf("the batch is not ordered by device first: %+v", p)
	}
	if p[1].ino != 100 || p[2].ino != 500 {
		t.Fatalf("the batch is not ordered by inode: %+v", p)
	}
	// Entries with no inode sort last and keep readdir order, which is the
	// best locality proxy available and is not the same as an inode sort.
	if p[3].dirSeq != 1 || p[4].dirSeq != 2 {
		t.Fatalf("entries without an inode lost their directory order: %+v", p)
	}
}

func TestAnEmptyNeedleMatchesEverything(t *testing.T) {
	root, _ := newRoot(t, []string{"a.txt", "b.txt"})
	res := walkFor(t, root, "")
	if len(res.Hits) < 2 {
		t.Fatalf("got %v, want every name", names(res))
	}
}

// The estimator's distinct-trigram term is what separates a CJK corpus from a
// Latin one at the same file count.
func TestTheEstimateGrowsWithDistinctTrigrams(t *testing.T) {
	latin := CorpusStats{
		Files: 100_000, NameBytesTotal: 2_000_000,
		DistinctTrigramsEst: 50_000, SampleCompressRatio: 0.3,
	}
	cjk := latin
	cjk.DistinctTrigramsEst = 2_000_000

	a := EstimateNameIndex(latin, 32)
	b := EstimateNameIndex(cjk, 32)
	if b.IndexBytes <= a.IndexBytes {
		t.Fatalf("a CJK corpus estimated %d bytes against %d for Latin", b.IndexBytes, a.IndexBytes)
	}
}

// A measured posting term is worth more than a modelled one, and the estimate
// says which it used.
func TestTheEstimateReportsItsConfidence(t *testing.T) {
	stats := CorpusStats{
		Files: 1000, NameBytesTotal: 20_000,
		DistinctTrigramsEst: 5_000, SampleCompressRatio: 0.3,
	}
	modelled := EstimateNameIndex(stats, 32)
	if modelled.Confidence != ConfidenceModelled {
		t.Fatalf("with no sample the estimate claimed %v", modelled.Confidence)
	}
	if !strings.Contains(modelled.Formula, "lower-confidence") {
		t.Fatalf("the formula does not warn: %q", modelled.Formula)
	}

	stats.PostingBytesPerBlock = 40
	measured := EstimateNameIndex(stats, 32)
	if measured.Confidence != ConfidenceMeasured {
		t.Fatalf("with a sample the estimate claimed %v", measured.Confidence)
	}
	// The formula carries the terms, so a wrong estimate shows which term was
	// wrong rather than only that it was.
	for _, want := range []string{"files", "blocks", "dict", "postings", "total"} {
		if !strings.Contains(measured.Formula, want) {
			t.Fatalf("the formula is missing the %s term: %q", want, measured.Formula)
		}
	}
}

func TestTheEstimateHandlesAnEmptyCorpus(t *testing.T) {
	got := EstimateNameIndex(CorpusStats{}, 32)
	if got.IndexBytes < HeaderBytes {
		t.Fatalf("an empty corpus estimated %d bytes, less than a header", got.IndexBytes)
	}
}
