//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

func newRoot(t *testing.T, files []string) *vfs.ShareRoot {
	t.Helper()
	host := t.TempDir()
	for _, rel := range files {
		full := filepath.Join(host, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
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
			t.Errorf("closing: %v", cerr)
		}
	})
	return root
}

func newService(t *testing.T, root *vfs.ShareRoot, ix *index.NameIndex) (*Service, []search.Source) {
	t.Helper()
	s := New(Options{
		Clock:   clock.Fixed(time.Unix(0, 1_700_000_000_000_000_000)),
		Storage: StorageSSD,
		CPUs:    2,
		Index:   ix,
	})
	return s, []search.Source{{Share: 1, Root: root}}
}

func TestTheWalkAnswersWithNoIndex(t *testing.T) {
	root := newRoot(t, []string{"data/report.txt", "data/photo.jpg"})
	s, src := newService(t, root, nil)

	res, err := s.Query(context.Background(), src, QueryOptions{Query: "report"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Tier != TierWalk {
		t.Fatalf("tier = %v, want the walk", res.Tier)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want the report", len(res.Hits))
	}
}

// A corrupt segment disables the index and leaves search working. It never
// fails a query, because the index is a cache.
func TestACorruptSegmentDisablesTheIndexAndSearchKeepsWorking(t *testing.T) {
	root := newRoot(t, []string{"data/report.txt"})

	dir := t.TempDir()
	// A base segment with the right magic and a header that lies about where
	// its sections are, which is what a bad disk produces.
	bad := make([]byte, index.HeaderLen)
	copy(bad, index.Magic)
	bad[4] = 1
	for i := 40; i < 48; i++ {
		bad[i] = 0xff
	}
	if err := os.WriteFile(filepath.Join(dir, "base.idx"), bad, 0o600); err != nil {
		t.Fatalf("writing the corrupt segment: %v", err)
	}

	// Opening it declines rather than failing.
	ix := OpenIndex(dir, index.DefaultConfig(), nil)
	if ix != nil {
		t.Fatal("a corrupt segment opened as a usable index")
	}

	// And search still answers.
	s, src := newService(t, root, ix)
	res, err := s.Query(context.Background(), src, QueryOptions{Query: "report"})
	if err != nil {
		t.Fatalf("a corrupt index failed the query: %v", err)
	}
	if res.Tier != TierWalk {
		t.Fatalf("tier = %v, want the walk", res.Tier)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want the report found by the walk", len(res.Hits))
	}
}

// The index answers when it can, and the result says which tier did.
func TestTheIndexAnswersAndSaysSo(t *testing.T) {
	root := newRoot(t, []string{"data/report.txt"})

	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	if aerr := ix.Append([]index.Entry{{Share: 1, Path: "data/report.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	s, src := newService(t, root, ix)
	res, qerr := s.Query(context.Background(), src, QueryOptions{Query: "report"})
	if qerr != nil {
		t.Fatalf("Query: %v", qerr)
	}
	if res.Tier != TierIndex {
		t.Fatalf("tier = %v, want the index", res.Tier)
	}
	if len(res.Hits) != 1 || res.Hits[0].Path != "data/report.txt" {
		t.Fatalf("got %+v, want the indexed hit", res.Hits)
	}
}

// A fallback runs the walk and reports why, so a caller can see the index
// exists and did not help rather than being told nothing matched.
func TestAFallbackRunsTheWalkAndSaysWhy(t *testing.T) {
	root := newRoot(t, []string{"data/ab.txt"})

	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	if aerr := ix.Append([]index.Entry{{Share: 1, Path: "data/ab.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	s, src := newService(t, root, ix)
	// Two bytes cannot form a trigram, so the index declines.
	res, qerr := s.Query(context.Background(), src, QueryOptions{Query: "ab"})
	if qerr != nil {
		t.Fatalf("Query: %v", qerr)
	}
	if res.Tier != TierWalk {
		t.Fatalf("tier = %v, want the walk", res.Tier)
	}
	if res.Fallback != index.FallbackQueryTooShort {
		t.Fatalf("fallback = %v, want QueryTooShort", res.Fallback)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want the walk to have found it anyway", len(res.Hits))
	}
}

// The index stores names only. A hit for a file that no longer exists is
// dropped, which is the staleness check the promoting stat doubles as.
func TestAStaleIndexHitIsDropped(t *testing.T) {
	root := newRoot(t, []string{"data/present.txt"})

	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	if aerr := ix.Append([]index.Entry{
		{Share: 1, Path: "data/present.txt"},
		{Share: 1, Path: "data/deleted.txt"},
	}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	s, src := newService(t, root, ix)
	res, qerr := s.Query(context.Background(), src, QueryOptions{Query: "txt"})
	if qerr != nil {
		t.Fatalf("Query: %v", qerr)
	}
	for _, h := range res.Hits {
		if h.Path == "data/deleted.txt" {
			t.Fatalf("an index entry for a file that does not exist was returned: %+v", res.Hits)
		}
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %+v, want only the file that is there", res.Hits)
	}
}

// A share the caller cannot see is dropped on the promote path too, which is
// the same existence rule the walk applies before scoring.
func TestAnIndexHitInAnInvisibleShareIsDropped(t *testing.T) {
	root := newRoot(t, []string{"data/report.txt"})

	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	if aerr := ix.Append([]index.Entry{
		{Share: 1, Path: "data/report.txt"},
		{Share: 99, Path: "data/report.txt"},
	}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	s, src := newService(t, root, ix)
	res, qerr := s.Query(context.Background(), src, QueryOptions{Query: "report"})
	if qerr != nil {
		t.Fatalf("Query: %v", qerr)
	}
	for _, h := range res.Hits {
		if h.Share == 99 {
			t.Fatalf("a hit in a share the caller cannot see was returned: %+v", res.Hits)
		}
	}
}

// The permission check runs on the promote path as well, so an indexed name
// the caller may not see never reaches them.
func TestAnIndexHitTheCallerCannotSeeIsDropped(t *testing.T) {
	root := newRoot(t, []string{"data/secret.txt"})

	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	if aerr := ix.Append([]index.Entry{{Share: 1, Path: "data/secret.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	s := New(Options{
		Clock: clock.Fixed(time.Unix(0, 1)), Storage: StorageSSD, CPUs: 2, Index: ix,
	})
	src := []search.Source{{
		Share: 1, Root: root,
		Allow: func(vfs.SafePath, bool) bool { return false },
	}}

	res, qerr := s.Query(context.Background(), src, QueryOptions{Query: "secret"})
	if qerr != nil {
		t.Fatalf("Query: %v", qerr)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("got %+v, want nothing the caller cannot see", res.Hits)
	}
}

// The concurrency gate refuses rather than queueing. Search sweeps whole
// trees, so letting every request start one starves interactive listings.
func TestTheConcurrencyGateRefuses(t *testing.T) {
	root := newRoot(t, []string{"data/report.txt"})
	s := New(Options{
		Clock: clock.Fixed(time.Unix(0, 1)), Storage: StorageRotational, CPUs: 1,
	})
	src := []search.Source{{Share: 1, Root: root}}

	// Fill every slot the storage class allows.
	for i := 0; i < StorageRotational.Concurrency(); i++ {
		s.slots <- struct{}{}
	}
	if _, err := s.Query(context.Background(), src, QueryOptions{Query: "report"}); err != ErrBusy {
		t.Fatalf("err = %v, want ErrBusy", err)
	}

	// And it works again once a slot frees.
	<-s.slots
	if _, err := s.Query(context.Background(), src, QueryOptions{Query: "report"}); err != nil {
		t.Fatalf("the query failed once a slot was free: %v", err)
	}
}

// Rotational storage gets fewer concurrent searches and a longer deadline.
// Fewer is a larger allowance, not a smaller one: four seek-bound walks on one
// array finish later than two and take the interactive requests with them.
func TestTheStorageClassMovesBothNumbers(t *testing.T) {
	if StorageRotational.Concurrency() >= StorageSSD.Concurrency() {
		t.Fatalf("rotational allows %d concurrent searches against %d for SSD",
			StorageRotational.Concurrency(), StorageSSD.Concurrency())
	}
	if StorageRotational.Deadline() <= StorageSSD.Deadline() {
		t.Fatalf("rotational deadline %v is not longer than the SSD's %v",
			StorageRotational.Deadline(), StorageSSD.Deadline())
	}
	if StorageRotational.Threads(16) >= StorageSSD.Threads(16) {
		t.Fatalf("rotational uses %d threads against %d for SSD",
			StorageRotational.Threads(16), StorageSSD.Threads(16))
	}
}

func TestAnOverlongQueryIsRefused(t *testing.T) {
	root := newRoot(t, []string{"data/report.txt"})
	s, src := newService(t, root, nil)

	long := make([]byte, 1<<20)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := s.Query(context.Background(), src, QueryOptions{Query: string(long)}); err == nil {
		t.Fatal("an overlong query was accepted")
	}
}

func TestACancelledQueryReportsCancellation(t *testing.T) {
	var files []string
	for i := 0; i < 100; i++ {
		files = append(files, fmt.Sprintf("d%02d/f%02d.txt", i%10, i))
	}
	root := newRoot(t, files)
	s, src := newService(t, root, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Query(ctx, src, QueryOptions{Query: "f"}); err != ErrCanceled {
		t.Fatalf("err = %v, want ErrCanceled", err)
	}
}

// Attaching and detaching the index at runtime is what the administrator's
// switch does.
func TestTheIndexCanBeSwitchedOffAtRuntime(t *testing.T) {
	root := newRoot(t, []string{"data/report.txt"})
	ix, err := index.Open(t.TempDir(), index.DefaultConfig())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	if aerr := ix.Append([]index.Entry{{Share: 1, Path: "data/report.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	s, src := newService(t, root, ix)
	res, qerr := s.Query(context.Background(), src, QueryOptions{Query: "report"})
	if qerr != nil || res.Tier != TierIndex {
		t.Fatalf("res = %+v, err = %v, want the index", res, qerr)
	}

	s.SetIndex(nil)
	res, qerr = s.Query(context.Background(), src, QueryOptions{Query: "report"})
	if qerr != nil {
		t.Fatalf("Query after switching off: %v", qerr)
	}
	if res.Tier != TierWalk {
		t.Fatalf("tier = %v, want the walk once the index is off", res.Tier)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want the walk to find it", len(res.Hits))
	}
}
