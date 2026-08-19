//go:build linux

package upload

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The sweep reads both sides before acting, so a session created between the
// two reads is not mistaken for an orphan, and an orphan additionally has to
// be older than the session lifetime before it is taken.

func TestTheSweepLeavesALiveSessionAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 4, SessionSpec{})

	rep, err := f.engine.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.ExpiredSessions != 0 || rep.OrphanParts != 0 {
		t.Fatalf("the sweep took %+v from a live session", rep)
	}
	if _, gerr := f.engine.Get(ctx, s.ID, testUser); gerr != nil {
		t.Fatalf("the live session did not survive the sweep: %v", gerr)
	}
	// The part file is still there, so a client that resumes finds its bytes.
	entries, rerr := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved)
	if rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) != 1 {
		t.Fatalf("the share holds %d entries, want the part file", len(entries))
	}
}

// A part file whose session row is gone, and which is older than the grace
// period, is the orphan the sweep exists for.
func TestTheSweepTakesAnOrphanedPartFileOnlyOnceItIsOldEnough(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 4, SessionSpec{})
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader([]byte("abcd")), nil); err != nil {
		t.Fatalf("PatchAt: %v", err)
	}

	// The row goes and the file stays, which is the shape a crash between the
	// two leaves behind.
	if err := f.store.State().DeleteUploadSession(ctx, s.ID.Bytes()); err != nil {
		t.Fatalf("removing the session row: %v", err)
	}
	f.engine.closeHandle(s.ID)

	// The sweep has nothing to go on but the file's age, and it is fresh, so
	// it could still be a session created while the sweep was running.
	rep, err := f.engine.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.OrphanParts != 0 {
		t.Fatalf("the sweep took a part file younger than the grace period: %+v", rep)
	}

	// Age it past the grace period and the same sweep takes it.
	part := f.partFileName(t)
	old := time.Unix(0, f.engine.clk.Nanos()-int64(limits.UploadSessionTTL)-int64(time.Hour))
	if cerr := os.Chtimes(filepath.Join(f.host, part), old, old); cerr != nil {
		t.Fatalf("ageing the part file: %v", cerr)
	}
	rep, err = f.engine.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.OrphanParts != 1 {
		t.Fatalf("the sweep took %+v, want one orphaned part file", rep)
	}
	if entries, rerr := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved); rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	} else if len(entries) != 0 {
		t.Fatalf("the share still holds %v", entries)
	}
}

// An expired session goes with its part file: the row and the file are one
// thing, and leaving either half is the debt the sweep exists to collect.
func TestTheSweepTakesAnExpiredSessionAndItsPartFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 4, SessionSpec{})

	// Move the clock past the session's lifetime rather than editing the row,
	// so what expires it is the same thing that expires one in the field.
	f.engine.clk = clock.Fixed(time.Unix(0, f.engine.clk.Nanos()+int64(limits.UploadSessionTTL)+1))

	rep, err := f.engine.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.ExpiredSessions != 1 {
		t.Fatalf("the sweep took %+v, want one expired session", rep)
	}
	if entries, rerr := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved); rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	} else if len(entries) != 0 {
		t.Fatalf("the expired session's part file survived: %v", entries)
	}
	if _, gerr := f.engine.Get(ctx, s.ID, testUser); gerr == nil {
		t.Fatal("the expired session row survived the sweep")
	}
}

func TestTheSweepTakesAnOrphanedSpoolDirectory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 6, SessionSpec{Mode: SpoolNameOrdered})
	if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 2,
		bytes.NewReader([]byte("def"))); err != nil {
		t.Fatalf("the spooled chunk: %v", err)
	}

	f.engine.clk = clock.Fixed(time.Unix(0, f.engine.clk.Nanos()+int64(limits.UploadSessionTTL)+1))
	rep, err := f.engine.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.ExpiredSessions != 1 {
		t.Fatalf("the sweep took %+v, want the expired session", rep)
	}
	// Both the part file and the spool directory with its chunk in it.
	if entries, rerr := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved); rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	} else if len(entries) != 0 {
		t.Fatalf("the expired name-ordered session left %v behind", entries)
	}
}

// partFileName is the one control file in the share root.
func (f *fixture) partFileName(t *testing.T) string {
	t.Helper()
	entries, err := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the share holds %d entries, want exactly the part file", len(entries))
	}
	return entries[0].Name
}
