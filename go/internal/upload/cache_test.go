//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The cache spool's proofs. The claims are: an upload through the cache
// publishes exactly what was sent, the cache is a window rather than a copy so
// nothing is left in it afterwards, a restart never reports an offset whose
// bytes are gone, and a client that leaves a hole is refused with a retry
// rather than being buffered without bound.

// cached turns the fixture's engine into one with a spool, switched on. The
// spool is a directory of its own so a test can look inside it.
func cached(t *testing.T, f *fixture) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "spool")
	c, err := openCache(dir)
	if err != nil {
		t.Fatalf("openCache: %v", err)
	}
	c.enabled.Store(true)
	f.engine.cache = c
	t.Cleanup(func() { closeOnce(t, f.engine) })
	return dir
}

// closeOnce closes an engine and tolerates having been closed already, for the
// tests that close one mid-body to simulate a restart.
func closeOnce(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("closing the engine: %v", err)
	}
}

// spoolFiles is every chunk file left in the spool, across every session.
func spoolFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the spool: %v", err)
	}
	return out
}

func TestACachedUploadPublishesExactlyAndLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)
	dir := cached(t, f)
	ctx := context.Background()
	body := []byte("0123456789")

	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{})
	// The mode is decided at creation and recorded, so nothing later has to
	// guess where this session's bytes are.
	r, lerr := f.engine.load(ctx, s.ID)
	if lerr != nil {
		t.Fatalf("load: %v", lerr)
	}
	if r.sess.CacheDir == "" {
		t.Fatal("the session was created without a cache directory while the cache was on")
	}

	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	// Nothing in the destination directory is listable while this runs.
	if got := f.names(t); len(got) != 0 {
		t.Fatalf("a listing during the upload shows %v, want nothing", got)
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}

	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("published %q, want %q", got, body)
	}
	// The window closed: the cache holds nothing for a session that finished.
	if left := spoolFiles(t, dir); len(left) != 0 {
		t.Fatalf("the spool still holds %v after the upload published", left)
	}
	if used := f.engine.cache.used.Load(); used != 0 {
		t.Fatalf("the cache accounts for %d bytes after the upload published, want 0", used)
	}
}

// The cache is a window over the file, not a copy of it: what it holds at any
// moment is bounded, and an upload larger than that bound still completes.
func TestACachedUploadLargerThanTheBudgetCompletes(t *testing.T) {
	f := newFixture(t)
	dir := cached(t, f)
	ctx := context.Background()

	const chunk = 4096
	const chunks = 16
	body := make([]byte, chunk*chunks)
	for i := range body {
		body[i] = byte(i)
	}

	s := f.create(t, "big.bin", uint64(len(body)), SessionSpec{})
	// A budget far below the file, so the merge has to keep up for this to
	// finish at all. It is set directly rather than by filling a disk: the
	// property under test is the bound, not statfs.
	f.engine.cache.limit.Store(chunk * 2)

	for i := 0; i < chunks; i++ {
		off := uint64(i * chunk)
		if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, off,
			bytes.NewReader(body[off:off+chunk]), nil); err != nil {
			t.Fatalf("chunk at %d: %v", off, err)
		}
		if used := f.engine.cache.used.Load(); used > chunk*3 {
			t.Fatalf("the cache holds %d bytes, past the budget of %d", used, chunk*2)
		}
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "big.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "big.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("the published file differs from what was sent")
	}
	if left := spoolFiles(t, dir); len(left) != 0 {
		t.Fatalf("the spool still holds %v", left)
	}
}

// A hole the client has not filled is the one case the merger cannot drain
// out of. Waiting would be waiting for the client, so the chunk is refused
// with a retry instead of buffering an unbounded hole.
func TestAFullCacheWithAHoleRefusesWithARetry(t *testing.T) {
	f := newFixture(t)
	cached(t, f)
	ctx := context.Background()

	const chunk = 4096
	body := make([]byte, chunk*4)
	s := f.create(t, "holey.bin", uint64(len(body)), SessionSpec{RandomAccess: true})
	f.engine.cache.limit.Store(chunk)

	// The second chunk lands first, so nothing is contiguous from zero and the
	// merger has nothing it can move.
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, chunk,
		bytes.NewReader(body[chunk:chunk*2]), nil); err != nil {
		t.Fatalf("the out-of-order chunk: %v", err)
	}
	_, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, chunk*2,
		bytes.NewReader(body[chunk*2:chunk*3]), nil)
	if !errors.Is(err, ErrCacheFull) {
		t.Fatalf("a chunk past a full cache with a hole below it gave %v, want ErrCacheFull", err)
	}
	var full *CacheFullError
	if !errors.As(err, &full) || full.RetryAfter <= 0 {
		t.Fatalf("the refusal carries no retry interval: %v", err)
	}

	// Filling the hole is what frees the cache, and the upload goes on.
	if _, perr := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body[:chunk]), nil); perr != nil {
		t.Fatalf("the chunk that fills the hole: %v", perr)
	}
	if _, perr := f.engine.PatchAt(ctx, f.root, s.ID, testUser, chunk*2,
		bytes.NewReader(body[chunk*2:chunk*3]), nil); perr != nil {
		t.Fatalf("the retried chunk: %v", perr)
	}
}

// The recommended spool is a tmpfs and a reboot empties one. A resuming client
// must never be told to resume past bytes that are gone, so what the cache no
// longer holds stops being reported.
func TestARestartDoesNotReportCachedBytesThatAreGone(t *testing.T) {
	f := newFixture(t)
	dir := cached(t, f)
	ctx := context.Background()

	const chunk = 4096
	body := make([]byte, chunk*2)
	for i := range body {
		body[i] = byte(i)
	}
	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{RandomAccess: true})

	// One chunk at the front, which the merger drains into the part file, and
	// one past a hole, which stays in the cache because nothing below it has
	// arrived.
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body[:chunk]), nil); err != nil {
		t.Fatalf("the first chunk: %v", err)
	}
	if err := f.engine.drainCache(ctx, s.ID); err != nil {
		t.Fatalf("draining: %v", err)
	}
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, chunk,
		bytes.NewReader(body[chunk:]), nil); err != nil {
		t.Fatalf("the second chunk: %v", err)
	}

	before, gerr := f.engine.Get(ctx, s.ID, testUser)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if before.Received != uint64(len(body)) {
		t.Fatalf("before the restart the session holds %d bytes, want %d", before.Received, len(body))
	}

	// The reboot: the tmpfs comes back empty.
	closeOnce(t, f.engine)
	if rerr := os.RemoveAll(dir); rerr != nil {
		t.Fatalf("emptying the spool: %v", rerr)
	}

	restarted, err := New(ctx, f.core, f.store.State(), Options{Clock: f.engine.clk, CacheDir: dir})
	if err != nil {
		t.Fatalf("reopening the engine: %v", err)
	}
	defer closeOnce(t, restarted)
	if rerr := restarted.RecoverCache(ctx); rerr != nil {
		t.Fatalf("RecoverCache: %v", rerr)
	}

	after, gerr := restarted.Get(ctx, s.ID, testUser)
	if gerr != nil {
		t.Fatalf("Get after the restart: %v", gerr)
	}
	// The merged prefix is in the part file and survives; the chunk that was
	// only ever in the cache does not, and is no longer claimed.
	if after.Offset != chunk {
		t.Fatalf("the resumable offset after the restart is %d, want %d", after.Offset, chunk)
	}
	if after.Received != chunk {
		t.Fatalf("the session claims %d bytes after the restart, want %d", after.Received, chunk)
	}

	// And the client can finish from there.
	if _, perr := restarted.PatchAt(ctx, f.root, s.ID, testUser, chunk,
		bytes.NewReader(body[chunk:]), nil); perr != nil {
		t.Fatalf("resuming: %v", perr)
	}
	if _, ferr := restarted.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("the published file differs from what was sent")
	}
}

// A session created while the cache was off keeps writing to the destination
// even after it is turned on: its bytes are in one place and a setting cannot
// move them.
func TestASessionInFlightKeepsTheModeItStartedWith(t *testing.T) {
	f := newFixture(t)
	dir := cached(t, f)
	ctx := context.Background()
	f.engine.cache.enabled.Store(false)

	body := []byte("0123456789")
	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{})
	f.engine.cache.enabled.Store(true)

	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	if left := spoolFiles(t, dir); len(left) != 0 {
		t.Fatalf("a session created before the switch spooled to the cache: %v", left)
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("published %q, want %q", got, body)
	}
}

// An abort takes the cache with it. The spool is the small volume, and holding
// a cancelled upload's window there until the sweep runs is what fills it.
func TestAnAbortReleasesTheCacheAtOnce(t *testing.T) {
	f := newFixture(t)
	dir := cached(t, f)
	ctx := context.Background()

	const chunk = 4096
	body := make([]byte, chunk*2)
	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{RandomAccess: true})
	// Past a hole, so it stays in the cache rather than being merged out.
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, chunk,
		bytes.NewReader(body[chunk:]), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	if left := spoolFiles(t, dir); len(left) == 0 {
		t.Fatal("the chunk never reached the cache")
	}
	if aerr := f.engine.Abort(ctx, s.ID, testUser); aerr != nil {
		t.Fatalf("Abort: %v", aerr)
	}
	if left := spoolFiles(t, dir); len(left) != 0 {
		t.Fatalf("an aborted session still holds %v in the cache", left)
	}
}

// The switch probes before it stores, so a spool that cannot take a file is
// refused where an administrator is watching.
func TestTheCacheSwitchRefusesAnUnwritableSpool(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "spool")
	c, err := openCache(dir)
	if err != nil {
		t.Fatalf("openCache: %v", err)
	}
	f.engine.cache = c
	t.Cleanup(func() { closeOnce(t, f.engine) })

	if serr := f.engine.SetCacheEnabled(context.Background(), true); serr != nil {
		t.Fatalf("turning the cache on over a writable spool: %v", serr)
	}
	if !f.engine.CacheEnabled() {
		t.Fatal("the switch reports off after being turned on")
	}

	if cerr := os.Chmod(dir, 0o500); cerr != nil {
		t.Fatalf("making the spool read-only: %v", cerr)
	}
	t.Cleanup(func() {
		if rerr := os.Chmod(dir, 0o700); rerr != nil {
			t.Errorf("restoring the spool mode: %v", rerr)
		}
	})
	if os.Geteuid() == 0 {
		t.Skip("running as root, which writes through a read-only directory mode")
	}
	if serr := f.engine.SetCacheEnabled(context.Background(), true); serr == nil {
		t.Fatal("an unwritable spool was accepted")
	}
}

// A chunk bigger than one merge step is drained across several of them, and
// the cache file survives until the last byte of it is in the part file.
// Deleting it after the first step throws away the tail.
func TestAChunkLargerThanAMergeStepIsNotLostPartWayThrough(t *testing.T) {
	f := newFixture(t)
	cached(t, f)
	ctx := context.Background()

	const step = 1024
	body := make([]byte, step*4+7)
	for i := range body {
		body[i] = byte(i*7 + 1)
	}
	f.engine.cache.step.Store(step)

	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{})
	// The merger is stopped so the steps are counted here rather than raced
	// against: the property is what one step leaves behind.
	f.engine.stopMerger(s.ID)
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	f.engine.stopMerger(s.ID)

	steps := 0
	for {
		moved, err := f.engine.mergeStep(ctx, s.ID)
		if err != nil {
			t.Fatalf("merge step %d: %v", steps, err)
		}
		if !moved {
			break
		}
		steps++
		if steps > 100 {
			t.Fatal("the merge did not converge")
		}
	}
	if steps < 2 {
		t.Fatalf("the chunk drained in %d step(s); the bound did not apply", steps)
	}

	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("published %d bytes, want %d; a multi-step merge lost part of the chunk",
			len(got), len(body))
	}
}

// A chunk file exists before its range is committed, because the range is only
// recorded once the checksum has been checked. Merging one early would put
// bytes the client is about to resend into the part file and then answer the
// resend from a frontier already past them.
func TestAChunkThatFailedItsChecksumIsNeverMerged(t *testing.T) {
	f := newFixture(t)
	cached(t, f)
	ctx := context.Background()

	body := []byte("0123456789")
	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{})

	wrong := Checksum{Algo: AlgoCRC32C, Digest: Sum(AlgoCRC32C, []byte("not what is being sent"))}
	_, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body), &wrong)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("a chunk with a wrong digest gave %v, want ErrChecksum", err)
	}

	// The merger is given every chance to take the bytes it must not take.
	if derr := f.engine.drainCache(ctx, s.ID); derr != nil {
		t.Fatalf("draining: %v", derr)
	}
	r, lerr := f.engine.load(ctx, s.ID)
	if lerr != nil {
		t.Fatalf("load: %v", lerr)
	}
	if r.sess.CacheMerged != 0 {
		t.Fatalf("the merge frontier moved to %d over an uncommitted chunk", r.sess.CacheMerged)
	}
	if got := r.set.ContiguousPrefix(); got != 0 {
		t.Fatalf("the session reports %d bytes after a failed checksum, want 0", got)
	}

	// The client resends the same range with the right digest, which is what
	// the refusal is for, and the upload completes with those bytes.
	right := Checksum{Algo: AlgoCRC32C, Digest: Sum(AlgoCRC32C, body)}
	if _, perr := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body), &right); perr != nil {
		t.Fatalf("the resent chunk: %v", perr)
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("published %q, want %q", got, body)
	}
}

// A chunk name carries its offset and nothing else has to be parsed to find
// out where it goes. The round trip is what the merger depends on.
func TestACacheChunkNameCarriesItsOffset(t *testing.T) {
	for _, off := range []uint64{0, 1, 4096, 1 << 40} {
		name := cacheChunkName(off)
		if !vfs.IsReservedName(name) {
			t.Fatalf("%q is listable", name)
		}
		got, ok := parseCacheChunkName(name)
		if !ok || got != off {
			t.Fatalf("%q parsed back as %d, %v; want %d", name, got, ok, off)
		}
	}
	for _, bad := range []string{"", "file.txt", ".scpart-", ".scpart-zzzz", ".scpart-" + "0"} {
		if _, ok := parseCacheChunkName(bad); ok {
			t.Fatalf("%q parsed as a cache chunk", bad)
		}
	}
}
