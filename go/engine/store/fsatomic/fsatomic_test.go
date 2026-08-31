package fsatomic

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

func TestReplaceFileDurableReplacesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, werr := f.Write([]byte("new content"))
		return werr
	}); err != nil {
		t.Fatalf("replacing: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new content" {
		t.Fatalf("file holds %q, want %q", got, "new content")
	}
	assertNoStagingResidue(t, dir)
}

func TestReplaceFileDurableFailingWriterLeavesOriginalUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passdb")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("writer gave up")
	err := ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		if _, werr := f.Write([]byte("half-written")); werr != nil {
			return werr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want the writer's own sentinel", err)
	}

	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "original" {
		t.Fatalf("a failed replace changed the file to %q", got)
	}
	assertNoStagingResidue(t, dir)
}

func TestReplaceFileDurableMissingDirectoryRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "index.db")
	err := ReplaceFileDurable(path, 0o600, func(*os.File) error {
		t.Fatal("write must not run when the directory does not exist")
		return nil
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v, want fs.ErrNotExist", err)
	}
}

func TestReplaceFileDurableConcurrentReplacesNeverTearTheFile(t *testing.T) {
	// Linux-only: Windows refuses to rename over a file another handle has
	// open, so concurrent replacement is not a contract it can offer.
	if runtime.GOOS != "linux" {
		t.Skip("rename-over-open is POSIX; this server is Linux-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	if err := os.WriteFile(path, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Each worker's payload is uniform, sized well past a single write's
	// usual buffer, and distinct from every other worker's, so a torn file
	// mixing two payloads shows up as a byte that does not match its own
	// payload's fill value.
	const workers = 8
	const size = 64 << 10
	contents := make([][]byte, workers)
	for i := range contents {
		contents[i] = make([]byte, size)
		for j := range contents[i] {
			contents[i][j] = byte('A' + i)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		task.Go(context.Background(), "concurrent-replace", func() {
			defer wg.Done()
			errs[i] = ReplaceFileDurable(path, 0o600, func(f *os.File) error {
				_, werr := f.Write(contents[i])
				return werr
			})
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != size {
		t.Fatalf("final content is %d bytes, want exactly %d: a torn or short write", len(got), size)
	}
	fill := got[0]
	for _, b := range got {
		if b != fill {
			t.Fatalf("final content mixes bytes from more than one worker: not %q throughout", fill)
		}
	}
	assertNoStagingResidue(t, dir)
}

// A reader that already holds the file open by descriptor keeps seeing the
// prior content through it after a replace, since POSIX unlink-on-open-
// handle semantics keep the old inode alive under the reader's descriptor
// once the rename has detached its name. A reader that opens by name after
// the replace sees only the new content.
func TestReplaceFileDurableIsAtomicToConcurrentReaders(t *testing.T) {
	// Linux-only, for the reason the comment above gives: the old inode
	// surviving under a reader's descriptor is POSIX behaviour, and Windows
	// refuses the rename outright while the reader holds the file open.
	if runtime.GOOS != "linux" {
		t.Skip("unlink-on-open-handle is POSIX; this server is Linux-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.entry")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}

	reader, oerr := os.Open(path)
	if oerr != nil {
		t.Fatal(oerr)
	}
	t.Cleanup(func() {
		if cerr := reader.Close(); cerr != nil {
			t.Errorf("closing the reader: %v", cerr)
		}
	})

	if rerr := ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, werr := f.Write([]byte("after"))
		return werr
	}); rerr != nil {
		t.Fatalf("replacing: %v", rerr)
	}

	before, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != "before" {
		t.Fatalf("the already-open reader saw %q, want the pre-replace content", before)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "after" {
		t.Fatalf("a fresh open saw %q, want the post-replace content", after)
	}
}

func assertNoStagingResidue(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) {
			t.Errorf("a staging file survived: %s", e.Name())
		}
	}
}
