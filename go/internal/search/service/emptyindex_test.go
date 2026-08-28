//go:build linux

package service

import (
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
)

// An index that is open but holds nothing must not answer.
//
// This is the state between an administrator enabling the index and a build
// filling it. The two are separate actions: the admin route stores the switch
// and applies it, and no build runs behind that call. In that window the index
// opens cleanly and sits nowhere near its entry ceiling, so nothing marked it
// unusable, and every query came back with zero hits reporting success from the
// index tier. Search answered "no such file" about every file that exists.
//
// Found by assembling the rebuilt services the way a deployment does, then
// probing this tree with the same shape to see whether it was inherited. It
// was, so it is fixed in both.
func TestAnEmptyIndexDoesNotAnswer(t *testing.T) {
	root := newRoot(t, []string{"reports/annual.txt", "notes.md"})

	// Opened, never built, which is what enabling the switch leaves behind.
	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("opening the index: %v", err)
	}
	s, src := newService(t, root, ix)

	res, err := s.Query(t.Context(), src, QueryOptions{Query: "annual", Limit: 10})
	if err != nil {
		t.Fatalf("querying: %v", err)
	}
	if res.Tier != TierWalk {
		t.Errorf("an empty index served the query from %s", res.Tier)
	}
	if len(res.Hits) != 1 {
		t.Errorf("the query found %d hits, want the file that exists", len(res.Hits))
	}
	// The reason travels with the result, so a caller can tell a fallback from
	// a genuinely empty corpus.
	if res.Fallback != index.FallbackIncomplete {
		t.Errorf("the fallback reason is %v, so nothing says why the index declined", res.Fallback)
	}
}

// Once built it answers, so the refusal above is about emptiness rather than a
// blanket refusal that would leave the index doing nothing at all.
func TestABuiltIndexStillAnswers(t *testing.T) {
	root := newRoot(t, []string{"reports/annual.txt", "notes.md"})
	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("opening the index: %v", err)
	}
	s, src := newService(t, root, ix)

	if _, berr := s.Build(t.Context(), src, func() bool { return true }, nil); berr != nil {
		t.Fatalf("building: %v", berr)
	}

	res, err := s.Query(t.Context(), src, QueryOptions{Query: "annual", Limit: 10})
	if err != nil {
		t.Fatalf("querying: %v", err)
	}
	if res.Tier != TierIndex {
		t.Errorf("a built index did not serve the query: %s", res.Tier)
	}
	if len(res.Hits) != 1 {
		t.Errorf("the built index found %d hits, want 1", len(res.Hits))
	}
}

// The state a running server reaches, built the way serve.go builds it rather
// than by handing the service an index directly.
//
// serve.go calls OpenIndex on <data_dir>/index and attaches whatever comes
// back, with no build behind it, which is exactly what flipping the switch
// does. This asserts the whole of that path rather than the Query method
// alone: OpenIndex has to return a usable index (not nil, or the fallback
// would be trivial), and the service over it has to still answer.
func TestTheServerPathWalksWithAFreshIndexDirectory(t *testing.T) {
	root := newRoot(t, []string{"reports/annual.txt", "notes.md"})
	dataDir := t.TempDir()

	// Exactly serve.go's call.
	ix := OpenIndex(filepath.Join(dataDir, "index"), index.DefaultConfig(), nil)
	if ix == nil {
		t.Fatal("OpenIndex refused a fresh directory, so the fallback below would prove nothing")
	}

	s, src := newService(t, root, ix)
	res, err := s.Query(t.Context(), src, QueryOptions{Query: "annual", Limit: 10})
	if err != nil {
		t.Fatalf("querying: %v", err)
	}
	if res.Tier != TierWalk {
		t.Errorf("a server that just enabled the index served from %s", res.Tier)
	}
	if len(res.Hits) != 1 {
		t.Errorf("the server found %d hits for a file that exists", len(res.Hits))
	}
}
