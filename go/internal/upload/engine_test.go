//go:build linux

package upload

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The engine's proofs. Three of them are the phase's own completion
// conditions: a finalize with a wrong whole-file digest leaves the session
// resumable with its part file intact, a chunk that fails its checksum leaves
// the interval set untouched, and a part file is unlistable for its whole
// life.

const testUser = core.UserID(42)

type fixture struct {
	engine *Engine
	core   *Core
	root   *vfs.ShareRoot
	host   string
	store  *store.Store
}

// Core is aliased so the fixture reads the way the package under test does.
type Core = core.Core

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	clk := clock.Fixed(time.Unix(0, 1_700_000_000_000_000_000))
	s, err := store.Open(dir, store.Options{Clock: clk})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})

	ev := acl.NewEvaluator()
	c, err := core.New(s, core.Options{ACL: ev, Clock: clk})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}

	host := filepath.Join(t.TempDir(), "share")
	if merr := os.MkdirAll(host, 0o775); merr != nil {
		t.Fatalf("creating the share: %v", merr)
	}
	if rerr := c.RegisterShare(context.Background(), core.ShareDef{
		ID: 1, Name: "docs", Host: host, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("RegisterShare: %v", rerr)
	}

	if serr := s.State().Write(context.Background(), func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(context.Background(),
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, 'tester', 'x', 0)`,
			int64(testUser)); uerr != nil {
			return uerr
		}
		_, gerr := tx.ExecContext(context.Background(),
			`INSERT INTO "grant"(user, share, subpath, allow, deny, inherit, label, created_ns)
			 VALUES (?, 1, '', ?, 0, 1, 'docs', 0)`,
			int64(testUser), int64(acl.Read|acl.Write|acl.Create|acl.Delete|acl.Download))
		return gerr
	}); serr != nil {
		t.Fatalf("seeding the account and its grant: %v", serr)
	}
	if lerr := ev.LoadFromState(context.Background(), s.State().SQL()); lerr != nil {
		t.Fatalf("loading grants: %v", lerr)
	}

	// The config seeds are passed as the product would pass them. They are
	// clamped to the compiled-in floor on the way in, which is why the tests
	// about the floor use real sizes rather than a floor of four bytes: the
	// point of a hard floor is that no caller can lower it.
	e, err := New(context.Background(), c, s.State(), Options{Clock: clk})
	if err != nil {
		t.Fatalf("upload.New: %v", err)
	}
	root, ok := c.ShareRoot(1)
	if !ok {
		t.Fatal("the share did not register")
	}
	return &fixture{engine: e, core: c, root: root, host: host, store: s}
}

// resolve is the permission-checked handle every engine call takes.
func (f *fixture) resolve(t *testing.T, name string) core.Resolved {
	t.Helper()
	p, err := vfs.ParseVpath("docs/" + name)
	if err != nil {
		t.Fatalf("ParseVpath: %v", err)
	}
	r, err := f.core.Resolve(testUser, p, acl.Write|acl.Create)
	if err != nil {
		t.Fatalf("Resolve %s: %v", name, err)
	}
	return r
}

func (f *fixture) create(t *testing.T, name string, total uint64, spec SessionSpec) Session {
	t.Helper()
	spec.TotalLen = &total
	s, err := f.engine.Create(context.Background(), f.resolve(t, name), spec)
	if err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	return s
}

// names is what an ordinary listing shows, which is what a user and a WebDAV
// client see.
func (f *fixture) names(t *testing.T) []string {
	t.Helper()
	entries, err := f.root.ReadDir(vfs.RootPath(), vfs.HideReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestOffsetAddressedUploadPublishesExactly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := []byte("0123456789")
	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{})

	// The part file exists immediately and is not in an ordinary listing, for
	// the whole duration of the upload.
	if got := f.names(t); len(got) != 0 {
		t.Fatalf("a listing during the upload shows %v, want nothing", got)
	}

	off, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader(body), nil)
	if err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	if off != uint64(len(body)) {
		t.Fatalf("offset after the write = %d, want %d", off, len(body))
	}

	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}

	got, err := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if err != nil {
		t.Fatalf("reading the published file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("published %q, want %q", got, body)
	}
	// Nothing is left over: the part file was renamed, not copied.
	if names := f.names(t); len(names) != 1 || names[0] != "file.bin" {
		t.Fatalf("the share holds %v, want just file.bin", names)
	}
	entries, err := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("a control-file listing holds %d entries, want 1", len(entries))
	}
}

// The phase's own condition: a finalize with a deliberately wrong whole-file
// digest fails and leaves the session resumable with the part file intact.
func TestFinalizeWithAWrongDigestKeepsTheSessionAndThePartFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := []byte("0123456789")

	wrong := Sum(AlgoBLAKE3, []byte("something else"))
	// Random-access, so the proof that the session still takes bytes below is
	// a rewrite of a range it already holds rather than an offset conflict.
	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{
		RandomAccess: true,
		Meta:         Meta{Verify: &Verify{Algo: AlgoBLAKE3, Digest: wrong}},
	})
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader(body), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}

	_, err := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID)
	if !errors.Is(err, ErrVerify) {
		t.Fatalf("Finalize returned %v, want ErrVerify", err)
	}

	// Nothing was published.
	if _, serr := os.Stat(filepath.Join(f.host, "file.bin")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("a file was published over a failed verification: %v", serr)
	}
	// The session is still resumable and still knows what it holds.
	after, gerr := f.engine.Get(ctx, s.ID, testUser)
	if gerr != nil {
		t.Fatalf("the session did not survive a failed verification: %v", gerr)
	}
	if after.Offset != uint64(len(body)) {
		t.Fatalf("the resumable offset is %d, want %d", after.Offset, len(body))
	}
	// The part file is still there with the bytes in it, so the client can
	// retry rather than resend a file it already uploaded.
	entries, rerr := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved)
	if rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name, ".scpart-") {
		t.Fatalf("the share holds %v, want the part file", entries)
	}
	part, jerr := vfs.RootPath().JoinControl(entries[0].Name)
	if jerr != nil {
		t.Fatalf("JoinControl: %v", jerr)
	}
	st, serr := f.root.Stat(part)
	if serr != nil {
		t.Fatalf("stat the part file: %v", serr)
	}
	if st.Size != uint64(len(body)) {
		t.Fatalf("the part file holds %d bytes, want %d", st.Size, len(body))
	}

	// And it still takes bytes, which is what "resumable" has to mean: the
	// client rewrites the range it thinks is wrong against the part file that
	// is still there, rather than opening a fresh session for a file it has
	// already uploaded once.
	if _, rerr := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(body), nil); rerr != nil {
		t.Fatalf("the resumed session refused a chunk: %v", rerr)
	}
	// The digest the client declared still does not describe these bytes, so
	// the refusal is the same one and the file is still not published.
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); !errors.Is(ferr, ErrVerify) {
		t.Fatalf("the second finalize returned %v, want ErrVerify", ferr)
	}
	if _, serr := os.Stat(filepath.Join(f.host, "file.bin")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("a file was published over a failed verification: %v", serr)
	}
}

// The verification that does match publishes, which is what proves the check
// above failed for the reason claimed rather than because it always fails.
func TestFinalizeWithTheRightDigestPublishes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := []byte("0123456789")

	s := f.create(t, "file.bin", uint64(len(body)), SessionSpec{
		Meta: Meta{Verify: &Verify{Algo: AlgoBLAKE3, Digest: Sum(AlgoBLAKE3, body)}},
	})
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader(body), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	if _, err := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("published %q, want %q", got, body)
	}
}

// The other completion condition: a chunk failing its checksum leaves the
// interval set untouched, so the client resends that range rather than
// resuming past a hole it thinks is filled.
func TestAFailedChunkChecksumLeavesTheIntervalSetAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 10, SessionSpec{})

	good := []byte("01234")
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader(good),
		&Checksum{Algo: AlgoCRC32C, Digest: Sum(AlgoCRC32C, good)}); err != nil {
		t.Fatalf("the first chunk: %v", err)
	}

	// A second chunk whose digest names different bytes.
	bad := []byte("56789")
	_, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 5, bytes.NewReader(bad),
		&Checksum{Algo: AlgoCRC32C, Digest: Sum(AlgoCRC32C, []byte("xxxxx"))})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("the mismatched chunk returned %v, want ErrChecksum", err)
	}

	after, gerr := f.engine.Get(ctx, s.ID, testUser)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if after.Offset != 5 {
		t.Fatalf("the resumable offset moved to %d over a failed checksum, want 5", after.Offset)
	}
	if after.Received != 5 {
		t.Fatalf("the set holds %d bytes over a failed checksum, want 5", after.Received)
	}

	// Finalize refuses and names what is missing, rather than publishing a
	// file with a hole in it.
	_, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID)
	var incomplete *IncompleteError
	if !errors.As(ferr, &incomplete) {
		t.Fatalf("Finalize returned %v, want an IncompleteError", ferr)
	}
	if len(incomplete.Missing) != 1 || incomplete.Missing[0] != (Range{Lo: 5, Hi: 10}) {
		t.Fatalf("the refusal names %v, want 5..10", incomplete.Missing)
	}

	// The same range resent with the right digest lands and finishes the file.
	if _, rerr := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 5, bytes.NewReader(bad),
		&Checksum{Algo: AlgoCRC32C, Digest: Sum(AlgoCRC32C, bad)}); rerr != nil {
		t.Fatalf("the resent chunk: %v", rerr)
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize after the resend: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "0123456789" {
		t.Fatalf("published %q, want 0123456789", got)
	}
}

func TestASessionBelongingToAnotherAccountReadsAsMissing(t *testing.T) {
	f := newFixture(t)
	s := f.create(t, "file.bin", 4, SessionSpec{})
	_, err := f.engine.Get(context.Background(), s.ID, testUser+1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account's Get returned %v, want ErrNotFound", err)
	}
}

func TestAnOutOfOrderChunkIsRefusedWithTheOffsetToResumeAt(t *testing.T) {
	f := newFixture(t)
	s := f.create(t, "file.bin", 20, SessionSpec{})
	_, err := f.engine.PatchAt(context.Background(), f.root, s.ID, testUser, 8,
		bytes.NewReader(make([]byte, 4)), nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("the out-of-order chunk returned %v, want a ConflictError", err)
	}
	if conflict.Expected != 0 || conflict.Got != 8 {
		t.Fatalf("the conflict says expected %d got %d, want 0 and 8", conflict.Expected, conflict.Got)
	}
}

func TestARandomAccessSessionTakesAnyOffset(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 10, SessionSpec{RandomAccess: true})

	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 5,
		bytes.NewReader([]byte("56789")), nil); err != nil {
		t.Fatalf("the far chunk: %v", err)
	}
	mid, err := f.engine.Get(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The bytes are on disk but the front is missing, so the resumable offset
	// is still zero while the received count is not.
	if mid.Offset != 0 || mid.Received != 5 {
		t.Fatalf("offset %d and received %d, want 0 and 5", mid.Offset, mid.Received)
	}
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader([]byte("01234")), nil); err != nil {
		t.Fatalf("the front chunk: %v", err)
	}
	if _, err := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "0123456789" {
		t.Fatalf("published %q, want 0123456789", got)
	}
}

func TestAChunkPastTheDeclaredLengthIsRefused(t *testing.T) {
	f := newFixture(t)
	s := f.create(t, "file.bin", 4, SessionSpec{})
	_, err := f.engine.PatchAt(context.Background(), f.root, s.ID, testUser, 0,
		bytes.NewReader([]byte("012345")), nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("the over-long body returned %v, want ErrTooLarge", err)
	}
}

// The floor applies mid-stream and not to the last chunk, and not to a file
// smaller than the floor: neither of those can be made bigger.
//
// The sizes here are the real floor rather than a lowered one, because the
// floor cannot be lowered. That is the point of it being compiled in.
func TestTheChunkFloorExemptsTheLastChunkAndASmallFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	floor := uint64(limits.UploadChunkFloor)

	s := f.create(t, "big.bin", floor+16, SessionSpec{})
	_, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader(make([]byte, 8)), nil)
	var small *ChunkTooSmallError
	if !errors.As(err, &small) {
		t.Fatalf("the short mid-stream chunk returned %v, want a ChunkTooSmallError", err)
	}
	if small.Min != floor {
		t.Fatalf("the refusal names a floor of %d, want %d", small.Min, floor)
	}

	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(make([]byte, floor)), nil); err != nil {
		t.Fatalf("the full-size chunk: %v", err)
	}
	// The last chunk is exempt even though it is under the floor.
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, floor,
		bytes.NewReader(make([]byte, 16)), nil); err != nil {
		t.Fatalf("the last chunk: %v", err)
	}

	// A whole file under the floor arrives in one short chunk.
	small2 := f.create(t, "small.bin", 3, SessionSpec{})
	if _, err := f.engine.PatchAt(ctx, f.root, small2.ID, testUser, 0,
		bytes.NewReader([]byte("abc")), nil); err != nil {
		t.Fatalf("the small whole file: %v", err)
	}
}

func TestPublishingOverAnExistingFileKeepsItsMode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// A file another program wrote, with a mode this server did not choose.
	// The mode is the point of the test: it is what the neighbours sharing the
	// directory read the file through, and it has to survive being replaced.
	existing := filepath.Join(f.host, "file.bin")
	if err := os.WriteFile(existing, []byte("old"), 0o640); err != nil { //nolint:gosec // G306 reads the mode: a fixture standing in for a file another program wrote.
		t.Fatalf("writing the existing file: %v", err)
	}

	s := f.create(t, "file.bin", 3, SessionSpec{})
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader([]byte("new")), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	if _, err := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st, err := os.Stat(existing)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Without the transplant the neighbours sharing this directory lose the
	// access they had a moment earlier.
	if got := st.Mode().Perm(); got != 0o640 {
		t.Fatalf("the replacement has mode %o, want 640", got)
	}
	body, rerr := os.ReadFile(existing)
	if rerr != nil {
		t.Fatalf("reading: %v", rerr)
	}
	if string(body) != "new" {
		t.Fatalf("the file holds %q, want new", body)
	}
}

func TestADeferredLengthIsRequiredBeforeFinalize(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := f.engine.Create(ctx, f.resolve(t, "file.bin"), SessionSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A deferred length means the floor cannot tell whether this is the last
	// chunk, so it is sent at the floor rather than under it.
	body := make([]byte, limits.UploadChunkFloor)
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader(body), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); !errors.Is(ferr, ErrBadRequest) {
		t.Fatalf("finalizing without a length returned %v, want ErrBadRequest", ferr)
	}
	if err := f.engine.SetLength(ctx, s.ID, testUser, uint64(len(body))); err != nil {
		t.Fatalf("SetLength: %v", err)
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize after the length arrived: %v", ferr)
	}
}

func TestSetLengthRefusesALengthShorterThanWhatLanded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := f.engine.Create(ctx, f.resolve(t, "file.bin"), SessionSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader(make([]byte, limits.UploadChunkFloor)), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}
	if err := f.engine.SetLength(ctx, s.ID, testUser, 4); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a length under what landed returned %v, want ErrBadRequest", err)
	}
}

func TestAbortLeavesThePartFileForTheSweep(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 4, SessionSpec{})
	if err := f.engine.Abort(ctx, s.ID, testUser); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if _, err := f.engine.Get(ctx, s.ID, testUser); err != nil {
		t.Fatalf("an aborted session vanished: %v", err)
	}
	// Aborting does not race a write already in flight: the file goes when the
	// sweep takes it, not underneath whoever is holding the descriptor.
	entries, rerr := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved)
	if rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) != 1 {
		t.Fatalf("the share holds %d entries after an abort, want the part file", len(entries))
	}
}

// Chunks of one file upload concurrently, and none blocks another's body.
//
// PatchAt used to hold the session's row lock across the body read. Several
// chunks in flight then serialised on it, which under HTTP/2 is a deadlock and
// not a queue: the blocked handlers never read their streams, the connection's
// flow-control window fills, and the chunk holding the lock cannot receive the
// rest of its own body. Every upload stopped after the first chunk.
//
// This proves the property that fixes it: a slow body does not stop another
// chunk from completing. The reader below blocks until the second chunk says
// it is done, so the test can only pass if they truly overlap.
func TestASlowChunkDoesNotBlockAnother(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = 4096
	s := f.create(t, "concurrent.bin", chunk*2, SessionSpec{RandomAccess: true})

	secondDone := make(chan struct{})
	firstErr := make(chan error, 1)

	// The first chunk's body yields nothing until the second has landed.
	task.Go(ctx, "slow chunk", func() {
		slow := io.MultiReader(
			readerFunc(func(p []byte) (int, error) {
				<-secondDone
				return 0, io.EOF
			}),
			bytes.NewReader(bytes.Repeat([]byte("a"), chunk)),
		)
		_, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, slow, nil)
		firstErr <- err
	})

	// Give the slow one time to take whatever it takes before this starts.
	time.Sleep(50 * time.Millisecond)
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, chunk,
		bytes.NewReader(bytes.Repeat([]byte("b"), chunk)), nil); err != nil {
		close(secondDone)
		t.Fatalf("the second chunk could not complete while the first was mid-body: %v", err)
	}
	close(secondDone)

	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("the first chunk failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first chunk never finished")
	}
}

// readerFunc adapts a function into an io.Reader.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
