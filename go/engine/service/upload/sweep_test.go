//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// An expired session takes its part file with it.
func TestTheSweepCollectsAnExpiredSession(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "abandoned.bin", uint64(limits.UploadChunkFloor), SessionSpec{})
	part, err := vfs.RootPath().JoinControl(partName(s.ID))
	if err != nil {
		t.Fatalf("naming the part file: %v", err)
	}

	f.clk.advance(limits.UploadSessionTTL * 2)
	rep, serr := f.engine.Sweep(ctx)
	if serr != nil {
		t.Fatalf("Sweep: %v", serr)
	}
	if rep.ExpiredSessions != 1 {
		t.Fatalf("the sweep collected %d expired sessions", rep.ExpiredSessions)
	}
	if _, gerr := f.engine.Get(ctx, s.ID, testUser); !errors.Is(gerr, ErrNotFound) {
		t.Fatalf("the expired session survived: %v", gerr)
	}
	if _, statErr := f.root(t).Stat(part); !errors.Is(statErr, vfs.ErrNotFound) {
		t.Fatalf("the expired session's part file survived: %v", statErr)
	}
	if n := f.engine.rowLockCount(); n != 0 {
		t.Fatalf("%d bookkeeping locks survived the sweep", n)
	}
}

// An orphan is a part file whose session row is already gone, which is why
// the sweep walks the directories a part file was ever created in rather than
// the live sessions.
func TestTheSweepCollectsAnOrphanedPartFileAfterTheGracePeriod(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "orphan.bin", uint64(limits.UploadChunkFloor), SessionSpec{})
	if derr := f.state.DeleteUploadSession(ctx, s.ID.Bytes()); derr != nil {
		t.Fatalf("deleting the session row: %v", derr)
	}

	// Inside the grace period the file is left alone: it may belong to a
	// session whose row is one transaction behind.
	rep, serr := f.engine.Sweep(ctx)
	if serr != nil {
		t.Fatalf("Sweep: %v", serr)
	}
	if rep.OrphanParts != 0 {
		t.Fatalf("the sweep took %d fresh part files", rep.OrphanParts)
	}

	ageFile(t, f, partName(s.ID), limits.UploadSessionTTL)
	rep, serr = f.engine.Sweep(ctx)
	if serr != nil {
		t.Fatalf("Sweep: %v", serr)
	}
	if rep.OrphanParts != 1 {
		t.Fatalf("the sweep collected %d orphaned part files", rep.OrphanParts)
	}
}

// A spool directory with no row is the same debt in directory form.
func TestTheSweepCollectsAnOrphanedSpoolDirectory(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "named.bin", uint64(limits.UploadChunkFloor*2), SessionSpec{Mode: SpoolNameOrdered})
	// One out-of-order chunk, so the spool directory exists with a file in it.
	if err := f.engine.PutNamed(ctx, f.root(t), s.ID, testUser, 2,
		readerOf(chunkOf(0, limits.UploadChunkFloor))); err != nil {
		t.Fatalf("PutNamed: %v", err)
	}
	if derr := f.state.DeleteUploadSession(ctx, s.ID.Bytes()); derr != nil {
		t.Fatalf("deleting the session row: %v", derr)
	}

	ageFile(t, f, spoolDirName(s.ID), limits.UploadSessionTTL)
	ageFile(t, f, partName(s.ID), limits.UploadSessionTTL)
	rep, serr := f.engine.Sweep(ctx)
	if serr != nil {
		t.Fatalf("Sweep: %v", serr)
	}
	if rep.OrphanSpools != 1 {
		t.Fatalf("the sweep collected %d orphaned spool directories", rep.OrphanSpools)
	}
	if _, err := os.Stat(filepath.Join(f.host, spoolDirName(s.ID))); !os.IsNotExist(err) {
		t.Fatalf("the orphaned spool directory survived: %v", err)
	}
}

// A live session's part file is never taken, however long the sweep runs.
func TestTheSweepNeverTakesALiveSessionsFiles(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "live.bin", uint64(limits.UploadChunkFloor), SessionSpec{})
	ageFile(t, f, partName(s.ID), limits.UploadSessionTTL)

	rep, serr := f.engine.Sweep(ctx)
	if serr != nil {
		t.Fatalf("Sweep: %v", serr)
	}
	if rep.OrphanParts != 0 || rep.ExpiredSessions != 0 {
		t.Fatalf("the sweep took a live session's files: %+v", rep)
	}
	if _, gerr := f.engine.Get(ctx, s.ID, testUser); gerr != nil {
		t.Fatalf("the live session is gone: %v", gerr)
	}
}

// ageFile moves a file's modification time far enough into the engine's own
// past that the sweep's grace period is crossed, without the test waiting a
// day.
//
// It is the engine's clock rather than the wall clock: the two are unrelated
// in a test, and the sweep compares a file's real modification time against
// the clock it was given.
func ageFile(t *testing.T, f *fixture, name string, by time.Duration) {
	t.Helper()
	path := filepath.Join(f.host, name)
	old := f.clk.Now().Add(-by - time.Minute)
	if cerr := os.Chtimes(path, old, old); cerr != nil {
		t.Fatalf("ageing %q: %v", name, cerr)
	}
}

func readerOf(b []byte) *bytes.Reader { return bytes.NewReader(b) }
