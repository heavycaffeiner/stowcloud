//go:build linux

package svc

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
)

// corpus builds a share root holding the named files, and reports the host
// directory beside it so a test can change the tree underneath the index the
// way another program would.
func corpus(t *testing.T, share uint32, names ...string) (search.Source, string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		full := filepath.Join(dir, filepath.FromSlash(n))
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
	return search.Source{Share: share, Root: root, Base: vfs.RootPath()}, dir
}

func newIndex(t *testing.T) *index.NameIndex {
	t.Helper()
	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	return ix
}

func hitPaths(hits []search.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Path)
	}
	slices.Sort(out)
	return out
}

// With no index attached, the walk answers and says so.
func TestQueryWalksWhenThereIsNoIndex(t *testing.T) {
	svc := New(Options{})
	src, _ := corpus(t, 1, "report.pdf", "other.txt")

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "report"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Tier != TierWalk {
		t.Errorf("tier is %v, want walk", res.Tier)
	}
	if want := []string{"report.pdf"}; !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("got %v, want %v", hitPaths(res.Hits), want)
	}
	if svc.HasIndex() {
		t.Error("HasIndex is true with no index attached")
	}
}

// A trigram query with an index behind it is answered from the index, and the
// tier reports truthfully so a caller can surface which one ran.
func TestQueryAnswersFromTheIndexWhenItCan(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "report.pdf", "other.txt")

	if err := ix.Append([]index.Entry{{Share: 1, Path: "report.pdf"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "report"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Tier != TierIndex {
		t.Errorf("tier is %v, want index", res.Tier)
	}
	if want := []string{"report.pdf"}; !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("got %v, want %v", hitPaths(res.Hits), want)
	}
}

// A query the index declines routes to the walk, and the reason is carried so
// it is not mistaken for an empty result.
func TestAShortQueryFallsBackToTheWalkAndReportsWhy(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "ab.txt")
	if err := ix.Append([]index.Entry{{Share: 1, Path: "ab.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "ab"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Tier != TierWalk {
		t.Errorf("tier is %v, want walk", res.Tier)
	}
	if res.Fallback != index.FallbackQueryTooShort {
		t.Errorf("fallback is %v, want QueryTooShort", res.Fallback)
	}
	if want := []string{"ab.txt"}; !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("the walk did not find the file: %v", hitPaths(res.Hits))
	}
}

// An incomplete index declines, the walk answers, and the walk is always
// current: this is the compensating chain's second link.
func TestAnIncompleteIndexFallsBackAndTheWalkStillFinds(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "report.pdf")
	ix.SetIncomplete(true)

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "report"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Fallback != index.FallbackIncomplete || res.Tier != TierWalk {
		t.Errorf("fallback %v on tier %v, want Incomplete on walk", res.Fallback, res.Tier)
	}
	if want := []string{"report.pdf"}; !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("the walk did not find a file the index never held: %v", hitPaths(res.Hits))
	}
}

// The gate refuses before any work starts, so a refused search costs a channel
// send rather than a directory read. The source below has no root at all, so
// anything that tried to read a directory would fail rather than answer.
func TestTheGateRefusesWithoutTouchingADirectory(t *testing.T) {
	svc := New(Options{})
	// Fill every slot so the next query has nowhere to go.
	for range limits.ConcurrentSearches {
		svc.slots <- struct{}{}
	}

	missing := search.Source{Share: 1, Base: vfs.RootPath()}
	_, err := svc.Query(t.Context(), []search.Source{missing}, QueryOptions{Query: "report"})
	if !errors.Is(err, ErrBusy) {
		t.Errorf("a saturated service returned %v, want ErrBusy", err)
	}
}

// A slot is released when a query finishes, or the service would answer ErrBusy
// forever after its first burst.
func TestTheGateReleasesItsSlot(t *testing.T) {
	svc := New(Options{})
	src, _ := corpus(t, 1, "report.pdf")
	for range limits.ConcurrentSearches + 2 {
		if _, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "report"}); err != nil {
			t.Fatalf("query: %v", err)
		}
	}
}

// The query length is a trust boundary: it is bounded before it is folded and
// split into trigrams.
func TestQueryLengthIsBounded(t *testing.T) {
	svc := New(Options{})
	long := strings.Repeat("a", limits.SearchQueryBytes+1)

	if _, err := svc.Query(t.Context(), nil, QueryOptions{Query: long}); err == nil {
		t.Error("an oversized query was accepted")
	}
}

// The limit is clamped rather than refused: a caller asking for more than the
// ceiling gets the ceiling.
func TestTheResultLimitIsClamped(t *testing.T) {
	svc := New(Options{})
	names := make([]string, 0, 12)
	for i := range 12 {
		names = append(names, "report-"+string(rune('a'+i))+".txt")
	}
	src, _ := corpus(t, 1, names...)

	res, err := svc.Query(t.Context(), []search.Source{src},
		QueryOptions{Query: "report", Limit: 3})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Hits) != 3 || !res.Truncated {
		t.Errorf("got %d hits, truncated %v; want 3 and true", len(res.Hits), res.Truncated)
	}

	res, err = svc.Query(t.Context(), []search.Source{src},
		QueryOptions{Query: "report", Limit: limits.SearchResults * 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Hits) != 12 {
		t.Errorf("got %d hits, want all 12", len(res.Hits))
	}
}

// An index hit for a file that no longer exists is dropped by the stat: index
// rows are yesterday's filesystem and today's decides.
func TestAStaleIndexHitIsDroppedByTheStat(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "present.txt")

	if err := ix.Append([]index.Entry{
		{Share: 1, Path: "present.txt"},
		{Share: 1, Path: "deleted.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: ".txt"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Tier != TierIndex {
		t.Fatalf("tier is %v, want index", res.Tier)
	}
	if want := []string{"present.txt"}; !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("got %v, want only the file that exists", hitPaths(res.Hits))
	}
}

// A hit outside the caller's Allow closure never surfaces, and neither does one
// for a share the caller cannot see.
func TestAnIndexHitOutsideAllowNeverSurfaces(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "public/a.txt", "private/b.txt")
	src.Allow = func(p vfs.SafePath, _ bool) bool {
		return !strings.HasPrefix(p.String(), "private")
	}

	if err := ix.Append([]index.Entry{
		{Share: 1, Path: "public/a.txt"},
		{Share: 1, Path: "private/b.txt"},
		{Share: 9, Path: "elsewhere.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: ".txt"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if want := []string{"public/a.txt"}; !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("got %v, want only the permitted path", hitPaths(res.Hits))
	}
}

// pathUnder is the revalidation itself: a stored path is rebuilt component by
// component under the source's base rather than trusted.
func TestPathUnderRefusesAnEscapingStoredPath(t *testing.T) {
	src, _ := corpus(t, 1, "a/b.txt")

	if _, err := pathUnder(src, "a/b.txt"); err != nil {
		t.Errorf("a legal stored path was refused: %v", err)
	}
	// A traversal component is refused outright: resolving it is the bypass.
	for _, stored := range []string{"../escape", "a/../../escape", "./a", "a/\x00b"} {
		if _, err := pathUnder(src, stored); err == nil {
			t.Errorf("pathUnder accepted %q", stored)
		}
	}
	// A leading separator produces empty components, which are skipped, so an
	// absolute-looking path is rebuilt as a relative one under the base rather
	// than reaching the host root.
	got, err := pathUnder(src, "/etc/passwd")
	if err != nil {
		t.Fatalf("an absolute-looking path was refused rather than contained: %v", err)
	}
	if got.String() != "etc/passwd" {
		t.Errorf("got %q, want it contained as etc/passwd", got.String())
	}
	if !got.Under(src.Base) {
		t.Error("the rebuilt path is not under the source's base")
	}
}

// A path that no longer joins under the base is dropped from the result.
func TestAnIndexHitThatNoLongerJoinsIsDropped(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "good.txt")

	if err := ix.Append([]index.Entry{
		{Share: 1, Path: "good.txt"},
		{Share: 1, Path: "../escaped.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	res, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: ".txt"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if want := []string{"good.txt"}; !slices.Equal(hitPaths(res.Hits), want) {
		t.Errorf("got %v, want only the path that still joins", hitPaths(res.Hits))
	}
}

// Metadata on the index path is resolved only for the survivors, and only when
// the caller asked.
func TestIndexHitsResolveMetadataOnlyOnRequest(t *testing.T) {
	ix := newIndex(t)
	svc := New(Options{Index: ix})
	src, _ := corpus(t, 1, "report.pdf")
	if err := ix.Append([]index.Entry{{Share: 1, Path: "report.pdf"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	bare, err := svc.Query(t.Context(), []search.Source{src}, QueryOptions{Query: "report"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if bare.Hits[0].Size != nil || bare.Hits[0].MTimeNs != nil {
		t.Error("a name-only query resolved metadata")
	}

	full, err := svc.Query(t.Context(), []search.Source{src},
		QueryOptions{Query: "report", WithMetadata: true})
	if err != nil {
		t.Fatalf("Query with metadata: %v", err)
	}
	if full.Hits[0].Size == nil || full.Hits[0].MTimeNs == nil {
		t.Error("metadata was requested and not resolved")
	}
}

// The bounds an administrator sets are the ones the next query uses: a setting
// that is stored and not read is a screen reporting a change that happened
// nowhere.
func TestSetBoundsIsReadBackAndMovesTheDeadline(t *testing.T) {
	svc := New(Options{})
	if got := svc.walkDeadline(); got != limits.SearchWalkDeadline {
		t.Errorf("default deadline is %v, want %v", got, limits.SearchWalkDeadline)
	}

	svc.SetBounds(3, 250*time.Millisecond)
	c, d := svc.Bounds()
	if c != 3 || d != 250*time.Millisecond {
		t.Errorf("read back %d and %v", c, d)
	}
	if got := svc.walkDeadline(); got != 250*time.Millisecond {
		t.Errorf("the deadline did not move: %v", got)
	}

	// Zero returns the field to the compiled-in default.
	svc.SetBounds(0, 0)
	if got := svc.walkDeadline(); got != limits.SearchWalkDeadline {
		t.Errorf("zero did not restore the default: %v", got)
	}
}

func TestSetBoundsChangesConcurrencyGate(t *testing.T) {
	svc := New(Options{})
	svc.SetBounds(2, 0)
	svc.slots <- struct{}{}
	svc.slots <- struct{}{}
	missing := search.Source{Share: 1, Base: vfs.RootPath()}
	_, err := svc.Query(t.Context(), []search.Source{missing}, QueryOptions{Query: "report"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy with concurrency 2, got %v", err)
	}
}

// The administrator's switch attaches and detaches the index at runtime.
func TestSetIndexAttachesAndDetaches(t *testing.T) {
	svc := New(Options{})
	if svc.HasIndex() {
		t.Error("a fresh service reports an index")
	}
	ix := newIndex(t)
	svc.SetIndex(ix)
	if !svc.HasIndex() {
		t.Error("SetIndex did not attach")
	}
	svc.SetIndex(nil)
	if svc.HasIndex() {
		t.Error("SetIndex(nil) did not detach")
	}
}
