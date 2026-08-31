//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The id is the whole of an upload URL, so a wrong length is refused rather
// than padded, and the refusal is the one an unknown session gets.
func TestSessionIDRoundTripsAndRefusesEveryOtherShape(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	back, err := ParseSessionID(id.String())
	if err != nil || back != id {
		t.Fatalf("the id round-tripped as %v, %v", back, err)
	}

	for name, wire := range map[string]string{
		"empty":      "",
		"truncated":  id.String()[:10],
		"oversized":  id.String() + id.String(),
		"padded":     id.String() + "==",
		"not base64": "................",
		"a path":     "../" + id.String(),
	} {
		if _, perr := ParseSessionID(wire); !errors.Is(perr, ErrNotFound) {
			t.Fatalf("a %s id returned %v", name, perr)
		}
	}
}

// The part file is one unlistable entry in the destination's own directory,
// sized sparsely up front so nothing is copied.
func TestCreateMakesOneUnlistablePartFileOfTheDeclaredSize(t *testing.T) {
	f := newFixture(t)
	sess := f.create(t, "report.txt", 4096, SessionSpec{})
	root := f.root(t)

	// Nothing shows in an ordinary listing for the whole of the upload.
	entries, err := root.ReadDir(vfs.RootPath(), vfs.HideReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("an upload in progress shows %v in a listing", entries)
	}

	part, err := vfs.RootPath().JoinControl(partName(sess.ID))
	if err != nil {
		t.Fatalf("naming the part file: %v", err)
	}
	st, err := root.Stat(part)
	if err != nil {
		t.Fatalf("the part file is not there: %v", err)
	}
	if st.Size != 4096 {
		t.Fatalf("the part file is %d bytes, want the declared 4096", st.Size)
	}

	// The name is the reserved one and nothing else: an earlier design
	// disguised it to get past component validation, which put part files in
	// every listing for the duration of every upload.
	if !strings.HasPrefix(partName(sess.ID), ".scpart-") {
		t.Fatalf("the part name is %q", partName(sess.ID))
	}
}

func TestCreateRefusesARootDestination(t *testing.T) {
	f := newFixture(t)
	p, err := vfs.ParseVpath("Docs")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	r, err := f.core.Resolve(testUser, p, 0)
	if err != nil {
		t.Fatalf("resolving the share root: %v", err)
	}
	total := uint64(10)
	if _, cerr := f.engine.Create(context.Background(), r,
		SessionSpec{TotalLen: &total}); !errors.Is(cerr, ErrBadRequest) {
		t.Fatalf("a session against the share root returned %v", cerr)
	}
}

// A session id is addressing, not authorization: every surface answers the
// same way for another account as for one that never existed.
func TestEverySurfaceIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	sess := f.create(t, "report.txt", 10, SessionSpec{})
	const stranger = core.UserID(999)

	if _, err := f.engine.Get(ctx, sess.ID, stranger); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get returned %v", err)
	}
	if _, err := f.engine.Offset(ctx, sess.ID, stranger); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Offset returned %v", err)
	}
	if err := f.engine.SetLength(ctx, sess.ID, stranger, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetLength returned %v", err)
	}
	if _, err := f.engine.PatchAt(ctx, f.root(t), sess.ID, stranger, 0,
		bytes.NewReader([]byte("x")), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PatchAt returned %v", err)
	}
	if _, err := f.engine.ListChunks(ctx, sess.ID, stranger); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListChunks returned %v", err)
	}
	if err := f.engine.Abort(ctx, sess.ID, stranger); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Abort returned %v", err)
	}
	if err := f.engine.BindAlias(ctx, "tid", stranger, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BindAlias returned %v", err)
	}
}

func TestADeferredLengthIsSuppliedOnceAndBoundsWhatLanded(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s, err := f.engine.Create(ctx, f.resolve(t, "deferred.bin"), SessionSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.TotalLen != nil {
		t.Fatalf("a deferred session declared %d", *s.TotalLen)
	}

	// A deferred-length session cannot know which chunk is its last, so every
	// chunk is measured against the floor.
	landed := uint64(limits.UploadChunkFloor)
	f.patch(t, s.ID, 0, bytes.Repeat([]byte("a"), int(landed)))

	// A length shorter than what already landed would make the session
	// complete over bytes past its own end.
	if serr := f.engine.SetLength(ctx, s.ID, testUser, landed-1); !errors.Is(serr, ErrBadRequest) {
		t.Fatalf("a length shorter than what landed returned %v", serr)
	}
	if serr := f.engine.SetLength(ctx, s.ID, testUser, landed); serr != nil {
		t.Fatalf("SetLength: %v", serr)
	}
	// Repeating the same length is idempotent; a different one is refused.
	if serr := f.engine.SetLength(ctx, s.ID, testUser, landed); serr != nil {
		t.Fatalf("repeating the same length returned %v", serr)
	}
	if serr := f.engine.SetLength(ctx, s.ID, testUser, landed*2); !errors.Is(serr, ErrBadRequest) {
		t.Fatalf("a second, different length returned %v", serr)
	}
}

// A finalize with no length refuses: the interval set cannot say whether a
// file of unknown size is complete.
func TestFinalizeWithoutALengthRefuses(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s, err := f.engine.Create(ctx, f.resolve(t, "deferred.bin"), SessionSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "deferred.bin"), s.ID); !errors.Is(ferr, ErrBadRequest) {
		t.Fatalf("a finalize with no declared length returned %v", ferr)
	}
}

// Abort takes the row, the handle and the bookkeeping lock. Leaving the lock
// for the sweep meant an aborted session's mutex sat in the map for a day.
func TestAbortForgetsTheRowLock(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "report.txt", 10, SessionSpec{})
	f.patch(t, s.ID, 0, []byte("0123456789"))

	if f.engine.rowLockCount() == 0 {
		t.Fatal("a session in flight holds no bookkeeping lock")
	}
	if err := f.engine.Abort(ctx, s.ID, testUser); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if n := f.engine.rowLockCount(); n != 0 {
		t.Fatalf("%d bookkeeping locks survived the abort", n)
	}

	// The row survives until the sweep takes the part file with it, and it
	// reads as aborted rather than as a session anything can still write to.
	got, err := f.engine.Get(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("Get on an aborted session returned %v", err)
	}
	if got.State != StateAborted {
		t.Fatalf("an aborted session reads as state %d", got.State)
	}
	if _, err := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 10,
		bytes.NewReader([]byte("x")), nil); err == nil {
		t.Fatal("a chunk against an aborted session was accepted")
	}
}

// Expiry is derived from the clock, so a session expires with no intervening
// write.
func TestExpiryIsDerivedFromTheClock(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "report.txt", 10, SessionSpec{})

	f.clk.advance(limits.UploadSessionTTL + time.Second)
	got, err := f.engine.Get(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateExpired {
		t.Fatalf("the session reads as state %d after its lifetime", got.State)
	}
	if _, perr := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 0,
		bytes.NewReader([]byte("x")), nil); !errors.Is(perr, ErrSessionExpired) {
		t.Fatalf("a chunk against an expired session returned %v", perr)
	}
}

// The floor is snapshotted at creation, so an administrator raising it
// mid-upload cannot retroactively refuse a chunk that was legal when sent.
func TestTheChunkFloorIsSnapshottedAtCreation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	total := uint64(limits.UploadChunkFloor * 3)
	s := f.create(t, "big.bin", total, SessionSpec{})

	raised := uint64(limits.UploadChunkFloor * 2)
	if err := f.engine.ApplySettings(ctx, &raised, &raised); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	// A chunk at the old floor is still legal for this session.
	f.patch(t, s.ID, 0, bytes.Repeat([]byte("a"), limits.UploadChunkFloor))

	// A session created now carries the new floor and refuses the same chunk.
	next := f.create(t, "other.bin", total, SessionSpec{})
	_, err := f.engine.PatchAt(ctx, f.root(t), next.ID, testUser, 0,
		bytes.NewReader(bytes.Repeat([]byte("a"), limits.UploadChunkFloor)), nil)
	var tooSmall *ChunkTooSmallError
	if !errors.As(err, &tooSmall) {
		t.Fatalf("a chunk below the new floor returned %v", err)
	}
	if tooSmall.Min != raised {
		t.Fatalf("the refusal names a floor of %d, want %d", tooSmall.Min, raised)
	}
}

func TestAnAliasRoundTripsAndRefusesAHostileTransferID(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "report.txt", 10, SessionSpec{})

	if err := f.engine.BindAlias(ctx, "transfer-1", testUser, s.ID); err != nil {
		t.Fatalf("BindAlias: %v", err)
	}
	a, err := f.engine.LookupAlias(ctx, "transfer-1", testUser)
	if err != nil {
		t.Fatalf("LookupAlias: %v", err)
	}
	if a.Session != s.ID || a.Share != testShare {
		t.Fatalf("the alias resolved to %+v", a)
	}

	// Rebinding would orphan the first session's spool with nothing naming
	// it, so it is refused rather than silently replaced.
	other := f.create(t, "second.txt", 10, SessionSpec{})
	if berr := f.engine.BindAlias(ctx, "transfer-1", testUser, other.ID); !errors.Is(berr, ErrAliasTaken) {
		t.Fatalf("a second bind returned %v", berr)
	}

	if uerr := f.engine.UnbindAlias(ctx, "transfer-1", testUser); uerr != nil {
		t.Fatalf("UnbindAlias: %v", uerr)
	}
	if _, lerr := f.engine.LookupAlias(ctx, "transfer-1", testUser); !errors.Is(lerr, ErrNotFound) {
		t.Fatalf("an unbound alias resolved: %v", lerr)
	}

	// The id arrives in a URL, so it is bounded and refused for the shapes
	// that cannot name anything, before it reaches a statement.
	for name, tid := range map[string]string{
		"empty":               "",
		"a separator":         "a/b",
		"a traversal":         "../../etc/passwd",
		"a control character": "a\x00b",
		"a newline":           "a\nb",
		"oversized":           strings.Repeat("a", limits.NameBytes+1),
	} {
		if berr := f.engine.BindAlias(ctx, tid, testUser, s.ID); berr == nil {
			t.Fatalf("a %s transfer id was accepted", name)
		}
	}
}
