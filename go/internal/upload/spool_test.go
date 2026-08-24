//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The name-ordered spool mode. The ordering rule is the whole difference from
// the offset-addressed one: a chunk carries a name rather than an offset, so
// where it lands is decided by what has already been assembled.

func TestNameOrderedChunksInOrderNeverTouchTheSpool(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 9, SessionSpec{Mode: SpoolNameOrdered})

	for i, part := range []string{"abc", "def", "ghi"} {
		if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, uint32(i+1),
			bytes.NewReader([]byte(part))); err != nil {
			t.Fatalf("chunk %d: %v", i+1, err)
		}
	}
	// Every chunk was the one expected next, so nothing was ever spooled and
	// the destination directory holds only the part file.
	entries, err := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind.IsDir() {
		t.Fatalf("the share holds %v, want just the part file", entries)
	}

	if _, err := f.engine.Assemble(ctx, f.resolve(t, "file.bin"), s.ID, 9, nil); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "abcdefghi" {
		t.Fatalf("published %q, want abcdefghi", got)
	}
}

// Chunks that arrive out of order are assembled by name and not by arrival,
// which is what the mode exists for.
func TestNameOrderedChunksAssembleByNameNotByArrival(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 9, SessionSpec{Mode: SpoolNameOrdered})

	// Deliberately backwards. Only the last one to arrive is the one the
	// assembly was waiting for, so the first two sit in the spool.
	for _, c := range []struct {
		name uint32
		body string
	}{{3, "ghi"}, {2, "def"}, {1, "abc"}} {
		if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, c.name,
			bytes.NewReader([]byte(c.body))); err != nil {
			t.Fatalf("chunk %d: %v", c.name, err)
		}
	}

	if _, err := f.engine.Assemble(ctx, f.resolve(t, "file.bin"), s.ID, 9, nil); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "abcdefghi" {
		t.Fatalf("published %q, want abcdefghi: the chunks were assembled in arrival order", got)
	}
	// The spool directory is gone with everything in it.
	if names := f.names(t); len(names) != 1 || names[0] != "file.bin" {
		t.Fatalf("the share holds %v, want just file.bin", names)
	}
	entries, err := f.root.ReadDir(vfs.RootPath(), vfs.IncludeReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("a control-file listing holds %v, want just the published file", entries)
	}
}

func TestASpooledChunkIsUnlistableWhileItWaits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 6, SessionSpec{Mode: SpoolNameOrdered})

	// Chunk two arrives first and waits for chunk one.
	if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 2, bytes.NewReader([]byte("def"))); err != nil {
		t.Fatalf("the out-of-order chunk: %v", err)
	}
	if names := f.names(t); len(names) != 0 {
		t.Fatalf("an ordinary listing shows %v during an upload, want nothing", names)
	}

	held, err := f.engine.ListChunks(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if !slices.Equal(held, []uint32{2}) {
		t.Fatalf("the session holds %v, want chunk 2", held)
	}
}

// A gap at assembly is a refusal rather than a truncated file: there is
// nothing left to wait for, so the missing name is named.
func TestAssemblyRefusesAMissingChunkName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 9, SessionSpec{Mode: SpoolNameOrdered})

	if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 1, bytes.NewReader([]byte("abc"))); err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	// Chunk 2 never arrives.
	if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 3, bytes.NewReader([]byte("ghi"))); err != nil {
		t.Fatalf("chunk 3: %v", err)
	}

	_, err := f.engine.Assemble(ctx, f.resolve(t, "file.bin"), s.ID, 9, nil)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("assembling over a gap returned %v, want ErrIncomplete", err)
	}
	if _, serr := os.Stat(filepath.Join(f.host, "file.bin")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("a file was published over a missing chunk: %v", serr)
	}
}

func TestAssemblyRefusesATotalThatDoesNotMatchWhatArrived(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 6, SessionSpec{Mode: SpoolNameOrdered})

	if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 1, bytes.NewReader([]byte("abc"))); err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	_, err := f.engine.Assemble(ctx, f.resolve(t, "file.bin"), s.ID, 6, nil)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a short assembly returned %v, want ErrBadRequest", err)
	}
}

// A repeated chunk name is a client retry, not a conflict: it is the same
// bytes, and refusing it strands a client that lost the response.
func TestARepeatedSpooledChunkNameOverwrites(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 6, SessionSpec{Mode: SpoolNameOrdered})

	for i := 0; i < 2; i++ {
		if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 2,
			bytes.NewReader([]byte("def"))); err != nil {
			t.Fatalf("the repeated chunk, attempt %d: %v", i+1, err)
		}
	}
	if err := f.engine.PutNamed(ctx, f.root, s.ID, testUser, 1, bytes.NewReader([]byte("abc"))); err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	if _, err := f.engine.Assemble(ctx, f.resolve(t, "file.bin"), s.ID, 6, nil); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "file.bin"))
	if rerr != nil {
		t.Fatalf("reading the published file: %v", rerr)
	}
	if string(got) != "abcdef" {
		t.Fatalf("published %q, want abcdef", got)
	}
}

// The modes do not accept each other's calls. The mode is a property of the
// session, chosen by the protocol that created it.
func TestTheTwoModesRefuseEachOthersCalls(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	offset := f.create(t, "a.bin", 4, SessionSpec{})
	if err := f.engine.PutNamed(ctx, f.root, offset.ID, testUser, 1,
		bytes.NewReader([]byte("abcd"))); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a named chunk against an offset-addressed session returned %v, want ErrBadRequest", err)
	}

	named := f.create(t, "b.bin", 4, SessionSpec{Mode: SpoolNameOrdered})
	if _, err := f.engine.PatchAt(ctx, f.root, named.ID, testUser, 0,
		bytes.NewReader([]byte("abcd")), nil); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("an offset patch against a name-ordered session returned %v, want ErrBadRequest", err)
	}
}
