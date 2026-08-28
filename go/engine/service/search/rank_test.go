package search

import (
	"testing"
	"time"
)

// The golden ordering the ranking exists to produce: an exact match above a
// prefix match, a prefix match above a plain substring, and a hidden file
// below its visible twin.
func TestRankingGoldenOrdering(t *testing.T) {
	needle := FoldString("report")
	score := func(name string) float32 {
		return Score(RankInput{NameFolded: FoldString(name), Needle: needle, Path: name})
	}

	exact := score("report")
	prefix := score("report-2024.pdf")
	substring := score("annual report.pdf")
	hidden := score(".report")

	if !(exact > prefix) {
		t.Errorf("an exact match (%v) should outrank a prefix match (%v)", exact, prefix)
	}
	if !(prefix > substring) {
		t.Errorf("a prefix match (%v) should outrank a substring match (%v)", prefix, substring)
	}
	if !(substring > hidden) {
		t.Errorf("a visible substring match (%v) should outrank a hidden exact-prefix one (%v)",
			substring, hidden)
	}
}

// An exact match is also a prefix match and the two weights add, which is
// deliberate: it is what puts an exact hit above everything.
func TestExactMatchTakesBothNameWeights(t *testing.T) {
	got := Score(RankInput{NameFolded: FoldString("report"), Needle: FoldString("report")})
	if want := WeightExact + WeightPrefix; got != want {
		t.Errorf("an exact match scored %v, want %v", got, want)
	}
}

// An empty needle awards no name term. A scoped listing with no query is
// expressed that way, and awarding a match to everything would rank nothing.
func TestEmptyNeedleAwardsNoNameTerm(t *testing.T) {
	if got := Score(RankInput{NameFolded: FoldString("anything")}); got != 0 {
		t.Errorf("an empty needle scored %v, want 0", got)
	}
}

// Recency is nil-aware on purpose: no stat means no guess, which is what keeps
// a name-only query from paying for metadata nobody asked for.
func TestRecencyIsZeroWithoutAStat(t *testing.T) {
	withoutStat := Score(RankInput{NameFolded: FoldString("a.txt"), Needle: FoldString("a.txt")})

	now := time.Now().UnixNano()
	fresh := now
	withStat := Score(RankInput{
		NameFolded: FoldString("a.txt"), Needle: FoldString("a.txt"),
		MTimeNs: &fresh, NowNs: now,
	})
	if !(withStat > withoutStat) {
		t.Errorf("a fresh file (%v) should outrank one with no stat (%v)", withStat, withoutStat)
	}
}

// The decay is linear over the window and stops at zero rather than going
// negative, so an old file is never pushed below an unstatted one.
func TestRecencyDecaysLinearlyAndFloorsAtZero(t *testing.T) {
	now := time.Now().UnixNano()
	at := func(age time.Duration) float32 {
		m := now - int64(age)
		return Score(RankInput{MTimeNs: &m, NowNs: now})
	}

	fresh := at(0)
	half := at(RecencyWindow / 2)
	old := at(RecencyWindow * 2)

	if !(fresh > half && half > old) {
		t.Errorf("recency did not decay: fresh %v, half %v, old %v", fresh, half, old)
	}
	if old != 0 {
		t.Errorf("a file past the window scored %v, want 0", old)
	}
	if diff := half - WeightRecency/2; diff > 0.01 || diff < -0.01 {
		t.Errorf("half a window scored %v, want about %v", half, WeightRecency/2)
	}
	// A clock that runs backwards must not award more than a fresh file.
	future := now + int64(time.Hour)
	if ahead := Score(RankInput{MTimeNs: &future, NowNs: now}); ahead != fresh {
		t.Errorf("a future mtime scored %v, want the fresh score %v", ahead, fresh)
	}
}

// Scope is component-aware, so "photo" does not match "photography".
func TestInScopeIsComponentAware(t *testing.T) {
	cases := []struct {
		path, scope string
		want        bool
	}{
		{"photo/a.jpg", "photo", true},
		{"photo", "photo", true},
		{"photography/a.jpg", "photo", false},
		{"photo/b/c.jpg", "photo", true},
		{"other/a.jpg", "photo", false},
		{"photo/a.jpg", "photo/", true},
		{"anything", "", true},
	}
	for _, c := range cases {
		if got := InScope(c.path, c.scope); got != c.want {
			t.Errorf("InScope(%q, %q) = %v, want %v", c.path, c.scope, got, c.want)
		}
	}
}

// An empty scope means the caller is not looking from anywhere in particular,
// so Score skips the bonus even though InScope reports true. Awarding it to
// everything would shift every score by a constant and make a scoreless
// candidate look ranked.
func TestScoreSkipsTheScopeBonusWhenNoScopeIsGiven(t *testing.T) {
	noScope := Score(RankInput{Path: "photo/a.jpg"})
	if noScope != 0 {
		t.Errorf("no scope and no needle scored %v, want 0", noScope)
	}
	withScope := Score(RankInput{Path: "photo/a.jpg", Scope: "photo"})
	if withScope != WeightInScope {
		t.Errorf("an in-scope path scored %v, want %v", withScope, WeightInScope)
	}
}

func TestHiddenIsPenalisedAndDetectedByTheDotConvention(t *testing.T) {
	if !IsHidden([]byte(".ssh")) || IsHidden([]byte("ssh")) || IsHidden(nil) {
		t.Error("IsHidden does not follow the dotfile convention")
	}
	visible := Score(RankInput{NameFolded: FoldString("cfg"), Needle: FoldString("cfg")})
	hidden := Score(RankInput{NameFolded: FoldString(".cfg"), Needle: FoldString("cfg")})
	if diff := visible - hidden; diff <= 0 {
		t.Errorf("a hidden file (%v) was not ranked below its visible twin (%v)", hidden, visible)
	}
}

// BM25 is always zero on the walk, which has no content, and is clamped so a
// caller cannot push a hit above the name terms with an out-of-range value.
func TestBM25IsClamped(t *testing.T) {
	for _, c := range []struct {
		in   float32
		want float32
	}{{-5, 0}, {0, 0}, {0.5, WeightBM25 * 0.5}, {1, WeightBM25}, {99, WeightBM25}} {
		if got := Score(RankInput{BM25: c.in}); got != c.want {
			t.Errorf("BM25 %v scored %v, want %v", c.in, got, c.want)
		}
	}
}
