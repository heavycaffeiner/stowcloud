//go:build linux

package upload

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The ordering rule and what survives a restart. A second engine over the same
// store is what a restart is: the in-memory handle cache is gone and every
// answer has to come back off disk.

func TestASessionResumesExactlyAfterARestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 10, SessionSpec{})

	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader([]byte("01234")), nil); err != nil {
		t.Fatalf("the first chunk: %v", err)
	}

	// The restart. The part file is reopened lazily from the stored name, and
	// the resumable offset comes off the interval rows rather than off the
	// part file's size, which on a sparse file says where the last write
	// landed and not what is in it.
	restarted, err := New(ctx, f.core, f.store.State(), Options{Clock: f.engine.clk})
	if err != nil {
		t.Fatalf("reopening the engine: %v", err)
	}

	after, gerr := restarted.Get(ctx, s.ID, testUser)
	if gerr != nil {
		t.Fatalf("the session did not survive the restart: %v", gerr)
	}
	if after.Offset != 5 {
		t.Fatalf("the resumable offset after a restart is %d, want 5", after.Offset)
	}

	if _, perr := restarted.PatchAt(ctx, f.root, s.ID, testUser, 5,
		bytes.NewReader([]byte("56789")), nil); perr != nil {
		t.Fatalf("the resumed chunk: %v", perr)
	}
	if _, ferr := restarted.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize after the restart: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "0123456789" {
		t.Fatalf("published %q, want 0123456789", got)
	}
}

// The ordering rule's own window: bytes on disk that were never committed to
// the set. A crash there must under-report, so the client resends the same
// bytes at the same offset and they land identically.
func TestBytesWrittenButNeverCommittedAreNotVisible(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 5, SessionSpec{})

	// The write phase without the commit phase, which is exactly what a crash
	// between the two leaves behind.
	part, perr := partPathOfSession(t, s)
	if perr != nil {
		t.Fatalf("naming the part file: %v", perr)
	}
	handle, herr := f.engine.handleFor(f.root, s.ID, part)
	if herr != nil {
		t.Fatalf("opening the part file: %v", herr)
	}
	if werr := writeAllAt(handle, []byte("hello"), 0); werr != nil {
		t.Fatalf("the uncommitted write: %v", werr)
	}

	restarted, err := New(ctx, f.core, f.store.State(), Options{Clock: f.engine.clk})
	if err != nil {
		t.Fatalf("reopening the engine: %v", err)
	}
	after, gerr := restarted.Get(ctx, s.ID, testUser)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if after.Offset != 0 {
		t.Fatalf("an uncommitted write is visible as offset %d, want 0", after.Offset)
	}

	// The client resends the same bytes at the same offset: idempotent.
	if _, rerr := restarted.PatchAt(ctx, f.root, s.ID, testUser, 0,
		bytes.NewReader([]byte("hello")), nil); rerr != nil {
		t.Fatalf("the resent chunk: %v", rerr)
	}
	if _, ferr := restarted.Finalize(ctx, f.resolve(t, "file.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "hello" {
		t.Fatalf("published %q, want hello", got)
	}
}

// A name-ordered session's spooled chunks are rows too, so a restart mid-
// assembly knows what it is still holding.
func TestANameOrderedSessionRemembersItsSpoolAcrossARestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 9, SessionSpec{Mode: SpoolNameOrdered})

	if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 3,
		bytes.NewReader([]byte("ghi"))); err != nil {
		t.Fatalf("chunk 3: %v", err)
	}

	restarted, err := New(ctx, f.core, f.store.State(), Options{Clock: f.engine.clk})
	if err != nil {
		t.Fatalf("reopening the engine: %v", err)
	}
	held, lerr := restarted.ListChunks(ctx, s.ID, testUser)
	if lerr != nil {
		t.Fatalf("ListChunks: %v", lerr)
	}
	if !slices.Equal(held, []uint32{3}) {
		t.Fatalf("after a restart the session holds %v, want chunk 3", held)
	}

	for _, c := range []struct {
		name uint32
		body string
	}{{1, "abc"}, {2, "def"}} {
		if perr := restarted.PutNamed(ctx, f.root, s.ID, testUser, c.name,
			bytes.NewReader([]byte(c.body))); perr != nil {
			t.Fatalf("chunk %d after the restart: %v", c.name, perr)
		}
	}
	if _, aerr := restarted.Assemble(ctx, f.resolve(t, "file.bin"), s.ID, 9, nil); aerr != nil {
		t.Fatalf("Assemble after the restart: %v", aerr)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "abcdefghi" {
		t.Fatalf("published %q, want abcdefghi", got)
	}
}

// partPathOfSession names a session's part file the way the engine does, so
// the test writes into the same file a chunk would.
func partPathOfSession(t *testing.T, s Session) (vfs.SafePath, error) {
	t.Helper()
	dest, err := vfs.ParseSafePath(s.Dest.String())
	if err != nil {
		return vfs.SafePath{}, err
	}
	return partPath(dest, partName(s.ID))
}
