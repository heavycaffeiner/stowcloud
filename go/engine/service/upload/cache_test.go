//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// cached is a fixture whose spool exists and is switched on.
func cached(t *testing.T) *fixture {
	t.Helper()
	spool := filepath.Join(t.TempDir(), "spool")
	f := newFixtureWithCache(t, spool)
	if err := f.engine.SetCacheEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetCacheEnabled: %v", err)
	}
	return f
}

func TestTheCacheSwitchIsReadAtCreationAndNeverAfterwards(t *testing.T) {
	ctx := context.Background()
	f := cached(t)
	const chunk = limits.UploadChunkFloor

	withCache := f.create(t, "cached.bin", uint64(chunk), SessionSpec{})
	if !withCache.Cached {
		t.Fatal("a session created with the cache on is not cached")
	}

	if err := f.engine.SetCacheEnabled(ctx, false); err != nil {
		t.Fatalf("SetCacheEnabled: %v", err)
	}
	// The session in flight keeps the mode it started in: its bytes are in one
	// place or the other and no switch moves them.
	still, err := f.engine.Get(ctx, withCache.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !still.Cached {
		t.Fatal("turning the switch off moved a session in flight")
	}

	direct := f.create(t, "direct.bin", uint64(chunk), SessionSpec{})
	if direct.Cached {
		t.Fatal("a session created with the cache off is cached")
	}
}

// A deployment with no spool has no switch to offer, and says so rather than
// pretending to have one.
func TestADeploymentWithNoSpoolRefusesTheSwitch(t *testing.T) {
	f := newFixture(t)
	if f.engine.CacheAvailable() {
		t.Fatal("a fixture with no spool reports one")
	}
	if err := f.engine.SetCacheEnabled(context.Background(), true); !errors.Is(err, ErrNoCache) {
		t.Fatalf("the switch on a deployment with no spool returned %v", err)
	}
}

// Name-ordered sessions never use the cache: they already spool out-of-order
// chunks to files of their own, so it would be a second staging layer under
// the first.
func TestNameOrderedSessionsAreNeverCached(t *testing.T) {
	f := cached(t)
	s := f.create(t, "named.bin", uint64(limits.UploadChunkFloor), SessionSpec{Mode: SpoolNameOrdered})
	if s.Cached {
		t.Fatal("a name-ordered session was given a cache directory")
	}
}

// A cached upload finishes: the bytes go to the spool, the merger drains them
// into the part file, and finalize publishes what was sent.
func TestACachedUploadPublishesWhatWasSent(t *testing.T) {
	ctx := context.Background()
	f := cached(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "cached.bin", uint64(chunk*2), SessionSpec{})

	first, second := chunkOf(0, chunk), chunkOf(uint64(chunk), chunk)
	f.patch(t, s.ID, 0, first)
	f.patch(t, s.ID, uint64(chunk), second)

	if _, err := f.engine.Finalize(ctx, f.resolve(t, "cached.bin"), s.ID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	want := append(append([]byte(nil), first...), second...)
	if got := readPublished(t, f, "cached.bin", len(want)); !bytes.Equal(got, want) {
		t.Fatal("the published bytes are not what was sent")
	}
	// Everything the session held in the spool is gone with it.
	if used := f.engine.cacheUsedForTest(); used != 0 {
		t.Fatalf("the spool still accounts for %d bytes", used)
	}
}

// The budget refuses a chunk that cannot fit and would not unblock the merge,
// and the refusal carries how long to wait, because what it waits for is a
// disk write already under way.
func TestTheBudgetRefusesWithARetryDelay(t *testing.T) {
	ctx := context.Background()
	f := cached(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "full.bin", uint64(chunk*4), SessionSpec{RandomAccess: true})

	// A first chunk lands, so the spool is holding something when the budget
	// is tightened: a budget checked against an empty spool always passes.
	f.patch(t, s.ID, uint64(chunk*3), chunkOf(uint64(chunk*3), chunk))

	// A budget of one byte: everything the spool holds is already over it.
	f.engine.setCacheBoundsForTest(1, 0)

	// A chunk past the contiguous prefix cannot unblock the merge, so it is
	// refused rather than buffered.
	_, err := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, uint64(chunk*2),
		bytes.NewReader(chunkOf(uint64(chunk*2), chunk)), nil)
	var full *CacheFullError
	if !errors.As(err, &full) {
		t.Fatalf("a chunk over the budget returned %v", err)
	}
	if full.RetryAfterSeconds <= 0 {
		t.Fatalf("the refusal asks the client to retry in %d seconds", full.RetryAfterSeconds)
	}

	// The chunk that would extend the contiguous region is taken anyway:
	// refusing it would refuse exactly the chunk that unblocks the merge.
	if _, perr := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 0,
		bytes.NewReader(chunkOf(0, chunk)), nil); perr != nil {
		t.Fatalf("the unblocking chunk was refused: %v", perr)
	}
}

// A chunk larger than one merge step drains across several of them, and the
// cache file goes only once every byte of it is in the part file.
func TestAChunkLargerThanAStepDrainsAcrossRounds(t *testing.T) {
	ctx := context.Background()
	f := cached(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "stepped.bin", uint64(chunk), SessionSpec{})

	// A step far smaller than the chunk, so one step cannot finish it.
	f.engine.setCacheBoundsForTest(0, 4096)
	body := chunkOf(0, chunk)
	f.patch(t, s.ID, 0, body)

	if _, err := f.engine.Finalize(ctx, f.resolve(t, "stepped.bin"), s.ID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if got := readPublished(t, f, "stepped.bin", len(body)); !bytes.Equal(got, body) {
		t.Fatal("a chunk drained across steps did not reassemble")
	}
}

// A file in the spool this build did not write is not merged as data and is
// not counted as a chunk.
func TestAForeignFileInTheSpoolIsNeverMerged(t *testing.T) {
	ctx := context.Background()
	spool := filepath.Join(t.TempDir(), "spool")
	f := newFixtureWithCache(t, spool)
	if err := f.engine.SetCacheEnabled(ctx, true); err != nil {
		t.Fatalf("SetCacheEnabled: %v", err)
	}
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "guarded.bin", uint64(chunk), SessionSpec{})
	body := chunkOf(0, chunk)
	f.patch(t, s.ID, 0, body)

	// A stray file inside the session's own cache directory, with a name that
	// does not parse as a chunk.
	dirs, err := os.ReadDir(spool)
	if err != nil {
		t.Fatalf("reading the spool: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("the spool holds %d directories", len(dirs))
	}
	stray := filepath.Join(spool, dirs[0].Name(), "not-a-chunk")
	if werr := os.WriteFile(stray, []byte("junk"), 0o600); werr != nil {
		t.Fatalf("planting a stray file: %v", werr)
	}

	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "guarded.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
	if got := readPublished(t, f, "guarded.bin", len(body)); !bytes.Equal(got, body) {
		t.Fatal("a stray file in the spool reached the published bytes")
	}
}

// A reboot empties the recommended spool, so a session whose recorded set
// still claimed those bytes would answer a resuming client with an offset
// whose data is gone.
func TestRecoveryCutsTheSetDownToWhatSurvived(t *testing.T) {
	ctx := context.Background()
	spool := filepath.Join(t.TempDir(), "spool")
	f := newFixtureWithCache(t, spool)
	if err := f.engine.SetCacheEnabled(ctx, true); err != nil {
		t.Fatalf("SetCacheEnabled: %v", err)
	}
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "resumed.bin", uint64(chunk*4), SessionSpec{RandomAccess: true})

	// A chunk well past the front, so nothing merges it: the contiguous
	// prefix never reaches it.
	f.patch(t, s.ID, uint64(chunk*2), chunkOf(uint64(chunk*2), chunk))
	before, err := f.engine.Get(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if before.Received != chunk {
		t.Fatalf("the session received %d before the reboot", before.Received)
	}

	// The spool is emptied the way a reboot of a memory filesystem empties it.
	dirs, rerr := os.ReadDir(spool)
	if rerr != nil {
		t.Fatalf("reading the spool: %v", rerr)
	}
	for _, d := range dirs {
		if werr := os.RemoveAll(filepath.Join(spool, d.Name())); werr != nil {
			t.Fatalf("emptying the spool: %v", werr)
		}
	}

	if recErr := f.engine.RecoverCache(ctx); recErr != nil {
		t.Fatalf("RecoverCache: %v", recErr)
	}
	after, err := f.engine.Get(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Received != 0 {
		t.Fatalf("the session still claims %d bytes that did not survive", after.Received)
	}
	if used := f.engine.cacheUsedForTest(); used != 0 {
		t.Fatalf("the rebuilt accounting is %d, want 0 for an empty spool", used)
	}
}

// A cache directory whose session row is gone is debt no walk of the shares
// can see: the spool is not a share, and the row is the only thing naming a
// directory in it.
func TestTheSweepCollectsAnOrphanedCacheDirectory(t *testing.T) {
	ctx := context.Background()
	spool := filepath.Join(t.TempDir(), "spool")
	f := newFixtureWithCache(t, spool)
	if err := f.engine.SetCacheEnabled(ctx, true); err != nil {
		t.Fatalf("SetCacheEnabled: %v", err)
	}
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "orphaned.bin", uint64(chunk*4), SessionSpec{RandomAccess: true})
	f.patch(t, s.ID, uint64(chunk*2), chunkOf(uint64(chunk*2), chunk))

	// The row goes and the directory stays, which is exactly the state the
	// sweep exists for.
	f.engine.stopMerger(s.ID)
	if derr := f.state.DeleteUploadSession(ctx, s.ID.Bytes()); derr != nil {
		t.Fatalf("deleting the session row: %v", derr)
	}

	rep, serr := f.engine.Sweep(ctx)
	if serr != nil {
		t.Fatalf("Sweep: %v", serr)
	}
	if rep.OrphanCaches != 1 {
		t.Fatalf("the sweep collected %d orphaned cache directories", rep.OrphanCaches)
	}
	dirs, rerr := os.ReadDir(spool)
	if rerr != nil {
		t.Fatalf("reading the spool: %v", rerr)
	}
	if len(dirs) != 0 {
		t.Fatalf("the spool still holds %d directories", len(dirs))
	}
}

// The spool is scratch space rather than a share: it borrows no id, and
// nothing that lists shares can reach it.
func TestTheSpoolIsScratchSpaceAndNotAShare(t *testing.T) {
	f := cached(t)
	if !f.engine.cache.root.IsScratch() {
		t.Fatal("the spool root does not report itself as scratch space")
	}
	// The share registry knows nothing about it: a lookup by the id a share
	// root carries finds the registered share, never the spool.
	for _, def := range f.core.Shares() {
		if def.ID == testShare {
			continue
		}
		t.Fatalf("the registry holds an unexpected share %+v", def)
	}
	if root, ok := f.core.ShareRoot(0); ok && root.IsScratch() {
		t.Fatal("the spool is reachable through the share registry")
	}
}
