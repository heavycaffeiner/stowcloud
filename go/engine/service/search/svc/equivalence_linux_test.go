//go:build linux

package svc

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// The index is a cache, and the property making it safe to enable is that it
// never changes an answer: a query the index serves should return what the walk
// would have returned over the same tree.
//
// Nothing else in this package asserted that. The surrounding tests check that
// the index answered, that a fallback happened, and that a stale hit is dropped,
// but none compares the two tiers against each other, so a defect in the trigram
// fold, the posting intersection, the block scan or the tree ordering would
// leave all of them passing while search quietly changed what it found.
//
// Running the comparison found two divergences, both inherited from the old
// tree and both recorded in the family document:
//
//   - the index matches the whole stored path while the walk matches only the
//     entry's own name, so a query naming a folder returns its contents from
//     one tier and the folder from the other;
//   - the walk matches directory names and the index holds none.
//
// They are pinned below rather than compared around, so changing either is a
// decision somebody makes rather than a surprise. What the equivalence test
// asserts is the part that does hold and that the index exists to get right:
// for a query that names a file rather than a folder, the two tiers agree
// exactly.

// writeFileForTest adds a file to a corpus after the index was built, the way
// another program writing into the share would.
func writeFileForTest(t *testing.T, dir, rel string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bothTiers runs a query with the index attached and again with it detached, so
// the only difference between the two results is which tier answered.
func bothTiers(t *testing.T, s *Service, sources []search.Source, q string) (indexed, walked Results) {
	t.Helper()

	indexed, err := s.Query(t.Context(), sources, QueryOptions{Query: q, Limit: 1000})
	if err != nil {
		t.Fatalf("the indexed query failed for %q: %v", q, err)
	}

	saved := s.index()
	s.SetIndex(nil)
	walked, err = s.Query(t.Context(), sources, QueryOptions{Query: q, Limit: 1000})
	s.SetIndex(saved)
	if err != nil {
		t.Fatalf("the walked query failed for %q: %v", q, err)
	}
	return indexed, walked
}

// A corpus with enough shape to exercise the index rather than a handful of
// names: nested directories, shared prefixes, repeated substrings, mixed case,
// and non-Latin names, which is where the byte-trigram choice earns itself.
func equivalenceCorpus() []string {
	return []string{
		"report.txt",
		"reports/annual.txt",
		"reports/annual-2024.txt",
		"reports/quarterly/q1.txt",
		"reports/quarterly/q2.txt",
		"Reports-UPPER.txt",
		"photos/holiday/beach.jpg",
		"photos/holiday/beach-2.jpg",
		"photos/holiday/sunset.jpg",
		"photos/family.jpg",
		"documents/notes.md",
		"documents/notes-old.md",
		"documents/archive/notes-2019.md",
		"budget.xlsx",
		"budget-draft.xlsx",
		"写真/旅行/海辺.jpg",
		"写真/家族.jpg",
		"文書/報告書.txt",
		"a.txt",
		"ab.txt",
		"abc.txt",
		"abcd.txt",
	}
}

// built returns a service whose index covers the corpus, and the host directory
// beside it.
func built(t *testing.T) (*Service, search.Source, string) {
	t.Helper()
	src, dir := corpus(t, 1, equivalenceCorpus()...)
	s := New(Options{Index: newIndex(t)})
	if _, err := s.Build(t.Context(), []search.Source{src}, func() bool { return true }, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s, src, dir
}

// The property the tiering rests on, over the queries where the two divergences
// above do not apply: a needle that appears in a file's own name and in no
// directory name on the way to it.
func TestTheIndexAnswersExactlyWhatTheWalkWould(t *testing.T) {
	s, src, _ := built(t)

	for _, q := range []string{
		// Each of these names a file rather than a folder in this corpus.
		"annual", "q1", "q2", "beach", "sunset", "family",
		"notes", "budget", "draft", "xlsx", "abcd",
		"海辺", "家族", "報告書",
		"Beach", "NOTES", "BUDGET",
		"nonexistent", "zzz",
	} {
		t.Run(q, func(t *testing.T) {
			indexed, walked := bothTiers(t, s, []search.Source{src}, q)

			gotIndexed := hitPaths(indexed.Hits)
			gotWalked := hitPaths(walked.Hits)

			if !slices.Equal(gotIndexed, gotWalked) {
				t.Errorf("the tiers disagree for %q\n  index (%s): %v\n  walk  (%s): %v",
					q, indexed.Tier, gotIndexed, walked.Tier, gotWalked)
			}
		})
	}
}

// The equivalence above means nothing unless the index actually answered. A
// service that fell back on every query would satisfy it trivially.
func TestTheIndexActuallyAnsweredDuringTheEquivalenceRun(t *testing.T) {
	s, src, _ := built(t)

	queries := []string{"annual", "beach", "notes", "budget", "abcd", "海辺"}
	served := 0
	for _, q := range queries {
		res, err := s.Query(t.Context(), []search.Source{src}, QueryOptions{Query: q, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if res.Tier == TierIndex {
			served++
		}
	}
	if served != len(queries) {
		t.Errorf("the index served %d of %d queries, so the equivalence test proves less than it appears",
			served, len(queries))
	}
}

// Agreement is worthless if both tiers agree on nothing. These are the files
// that exist, so a fold that dropped entries fails here rather than passing by
// returning two empty sets.
func TestBothTiersFindTheFilesThatExist(t *testing.T) {
	s, src, _ := built(t)

	for _, c := range []struct {
		query string
		want  []string
	}{
		{"sunset", []string{"photos/holiday/sunset.jpg"}},
		{"海辺", []string{"写真/旅行/海辺.jpg"}},
		{"報告書", []string{"文書/報告書.txt"}},
		{"annual", []string{"reports/annual-2024.txt", "reports/annual.txt"}},
	} {
		t.Run(c.query, func(t *testing.T) {
			indexed, walked := bothTiers(t, s, []search.Source{src}, c.query)
			want := slices.Clone(c.want)
			slices.Sort(want)

			if got := hitPaths(indexed.Hits); !slices.Equal(got, want) {
				t.Errorf("the index found %v, want %v", got, want)
			}
			if got := hitPaths(walked.Hits); !slices.Equal(got, want) {
				t.Errorf("the walk found %v, want %v", got, want)
			}
		})
	}
}

// The first divergence, pinned. The index searches the whole stored path, so a
// query naming a folder returns its contents; the walk tests the entry's own
// name, so it returns the folder.
//
// Changing either side is a product decision the family document sets out. This
// fails when one is changed without the other, which is the point.
func TestTheIndexMatchesThePathAndTheWalkMatchesTheName(t *testing.T) {
	s, src, _ := built(t)

	indexed, walked := bothTiers(t, s, []search.Source{src}, "holiday")

	// The index returns the files beneath the folder, because their stored
	// paths contain the needle.
	wantIndexed := []string{
		"photos/holiday/beach-2.jpg",
		"photos/holiday/beach.jpg",
		"photos/holiday/sunset.jpg",
	}
	slices.Sort(wantIndexed)
	if got := hitPaths(indexed.Hits); !slices.Equal(got, wantIndexed) {
		t.Errorf("the index matched %v, want the files under the folder %v", got, wantIndexed)
	}

	// The walk returns the folder itself and nothing under it, because only its
	// own name contains the needle.
	if got := hitPaths(walked.Hits); !slices.Equal(got, []string{"photos/holiday"}) {
		t.Errorf("the walk matched %v, want just the folder", got)
	}
}

// The second divergence, pinned: the walk can return a directory hit and the
// index never does.
func TestTheWalkMatchesDirectoriesAndTheIndexDoesNot(t *testing.T) {
	s, src, _ := built(t)

	indexed, walked := bothTiers(t, s, []search.Source{src}, "quarterly")

	var walkedDir bool
	for _, h := range walked.Hits {
		if h.IsDir && strings.HasSuffix(h.Path, "quarterly") {
			walkedDir = true
		}
	}
	if !walkedDir {
		t.Error("the walk no longer matches a directory by name, which changes the contract this pins")
	}

	for _, h := range indexed.Hits {
		if h.IsDir {
			t.Errorf("the index now holds a directory name (%s), which is the change the family document describes", h.Path)
		}
	}
}

// A file added after the build is absent from the index and present to the
// walk, which is why a stale index is a slower answer rather than a wrong one:
// the caller is told which tier answered.
func TestAFileAddedAfterTheBuildIsFoundByTheWalk(t *testing.T) {
	s, src, dir := built(t)
	writeFileForTest(t, dir, "reports/brandnew.txt")

	indexed, walked := bothTiers(t, s, []search.Source{src}, "brandnew")

	if got := hitPaths(indexed.Hits); len(got) != 0 {
		t.Errorf("the index reported a file added after it was built: %v", got)
	}
	if got := hitPaths(walked.Hits); !slices.Contains(got, "reports/brandnew.txt") {
		t.Errorf("the walk did not find a file that exists: %v", got)
	}
}

// A rebuild closes that gap, which is what makes keeping the index current
// worthwhile rather than discarding it.
func TestARebuildFindsWhatWasAddedAfterTheFirstOne(t *testing.T) {
	s, src, dir := built(t)
	writeFileForTest(t, dir, "reports/brandnew.txt")

	if _, err := s.Build(t.Context(), []search.Source{src}, func() bool { return true }, nil); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	indexed, walked := bothTiers(t, s, []search.Source{src}, "brandnew")
	if !slices.Equal(hitPaths(indexed.Hits), hitPaths(walked.Hits)) {
		t.Errorf("after a rebuild the tiers still disagree\n  index: %v\n  walk:  %v",
			hitPaths(indexed.Hits), hitPaths(walked.Hits))
	}
	if !slices.Contains(hitPaths(indexed.Hits), "reports/brandnew.txt") {
		t.Errorf("the rebuilt index does not hold the new file: %v", hitPaths(indexed.Hits))
	}
}

// Folding happens at index time and again on the query, so both sides meet in
// one space and a query in the other case finds the same files through either
// tier.
func TestCaseFoldingAgreesAcrossTheTiers(t *testing.T) {
	s, src, _ := built(t)

	lowerIndexed, lowerWalked := bothTiers(t, s, []search.Source{src}, "beach")
	upperIndexed, upperWalked := bothTiers(t, s, []search.Source{src}, "BEACH")

	if !slices.Equal(hitPaths(lowerIndexed.Hits), hitPaths(upperIndexed.Hits)) {
		t.Errorf("the index folds case differently by query\n  lower: %v\n  upper: %v",
			hitPaths(lowerIndexed.Hits), hitPaths(upperIndexed.Hits))
	}
	if !slices.Equal(hitPaths(lowerWalked.Hits), hitPaths(upperWalked.Hits)) {
		t.Errorf("the walk folds case differently by query\n  lower: %v\n  upper: %v",
			hitPaths(lowerWalked.Hits), hitPaths(upperWalked.Hits))
	}
	// And the fold actually matched something, or the agreement above is two
	// empty sets.
	if len(lowerIndexed.Hits) == 0 {
		t.Error("the fold matched nothing, so this proves nothing about folding")
	}
}

// A mixed-case filename is found by a lower-case query through both tiers,
// which is what folding at index time is for.
func TestAMixedCaseFilenameIsFoundByEitherTier(t *testing.T) {
	s, src, _ := built(t)

	indexed, walked := bothTiers(t, s, []search.Source{src}, "reports-upper")

	for _, c := range []struct {
		tier string
		got  []string
	}{{"index", hitPaths(indexed.Hits)}, {"walk", hitPaths(walked.Hits)}} {
		if !slices.Contains(c.got, "Reports-UPPER.txt") {
			t.Errorf("the %s tier did not fold a mixed-case filename: %v", c.tier, c.got)
		}
	}
}
