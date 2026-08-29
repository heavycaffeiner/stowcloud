//go:build linux

package core

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// resolveAt resolves a path under the readable share the read tests share.
func resolveAt(t *testing.T, c *Core, path string, need acl.Perms) Resolved {
	t.Helper()
	r, err := c.Resolve(1, vpath(t, path), need)
	if err != nil {
		t.Fatalf("resolving %q: %v", path, err)
	}
	return r
}

func openStream(t *testing.T, c *Core, r Resolved, rng *[2]uint64) (FidEntry, *Stream) {
	t.Helper()
	entry, s, err := c.OpenStream(context.Background(), r, rng)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	return entry, s
}

func drain(t *testing.T, s *Stream) string {
	t.Helper()
	b, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("draining the stream: %v", err)
	}
	return string(b)
}

func TestAWholeFileStreamsExactlyItsContent(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "readme.txt", "hello world")
	r := resolveAt(t, c, "Documents/readme.txt", acl.Download)

	entry, s, err := c.OpenStream(context.Background(), r, nil)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer closeStream(t, s)

	if entry.Name != "readme.txt" || entry.Size != 11 {
		t.Fatalf("the fid entry is %+v, want readme.txt of 11 bytes", entry)
	}
	if entry.ETag == "" {
		t.Fatal("the fid entry carries no validator")
	}
	if s.Remaining() != 11 {
		t.Fatalf("Remaining before the read is %d, want 11", s.Remaining())
	}

	head := make([]byte, 5)
	if n, rerr := s.Read(head); n != 5 || rerr != nil {
		t.Fatalf("reading the head = %d, %v", n, rerr)
	}
	if s.Remaining() != 6 {
		t.Fatalf("Remaining mid-stream is %d, want 6", s.Remaining())
	}
	if rest := drain(t, s); string(head)+rest != "hello world" {
		t.Fatalf("the stream produced %q%q", head, rest)
	}
	if s.Remaining() != 0 {
		t.Fatalf("Remaining after the drain is %d", s.Remaining())
	}
}

func TestRangeClamping(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "abc.txt", "abcdefghij")
	r := resolveAt(t, c, "Documents/abc.txt", acl.Download)

	cases := []struct {
		name  string
		rng   [2]uint64
		want  string
		bytes uint64
	}{
		{"an inclusive middle range", [2]uint64{2, 4}, "cde", 3},
		{"an end past the size clamps to the size", [2]uint64{5, 999}, "fghij", 5},
		{"a start past the size is empty", [2]uint64{99, 200}, "", 0},
		{"a start past the end is empty", [2]uint64{6, 3}, "", 0},
		{"the maximum end saturates rather than wrapping", [2]uint64{0, ^uint64(0)}, "abcdefghij", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rng := tc.rng
			_, s := openStream(t, c, r, &rng)
			defer closeStream(t, s)
			if s.Remaining() != tc.bytes {
				t.Fatalf("Remaining is %d, want %d", s.Remaining(), tc.bytes)
			}
			if got := drain(t, s); got != tc.want {
				t.Fatalf("the stream produced %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadsAreChunkedWhateverTheBuffer(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "big.bin", strings.Repeat("a", streamChunk+4096))
	r := resolveAt(t, c, "Documents/big.bin", acl.Download)
	_, s := openStream(t, c, r, nil)
	defer closeStream(t, s)

	buf := make([]byte, streamChunk*2)
	n, err := s.Read(buf)
	if err != nil {
		t.Fatalf("the first read: %v", err)
	}
	if n != streamChunk {
		t.Fatalf("a read with a %d-byte buffer returned %d bytes, want the %d chunk",
			len(buf), n, streamChunk)
	}
}

func TestAFileTruncatedAfterOpenEndsAtTheShortLength(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "shrink.txt", strings.Repeat("x", 4096))
	r := resolveAt(t, c, "Documents/shrink.txt", acl.Download)
	_, s := openStream(t, c, r, nil)
	defer closeStream(t, s)

	head := make([]byte, 16)
	if _, err := s.Read(head); err != nil {
		t.Fatalf("the first read: %v", err)
	}
	if err := os.Truncate(filepath.Join(host, "shrink.txt"), 16); err != nil {
		t.Fatalf("truncating the file: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("reading past a truncation = %d, %v, want 0 and io.EOF", n, err)
	}
	if s.Remaining() != 0 {
		t.Fatalf("Remaining after the truncation is %d, want 0", s.Remaining())
	}
}

// TestAnAtomicReplaceDoesNotChangeWhatIsBeingRead is the descriptor-for-life
// promise: a rename over the name mid-download must not splice two files
// into one response body.
func TestAnAtomicReplaceDoesNotChangeWhatIsBeingRead(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "live.txt", "original content")
	r := resolveAt(t, c, "Documents/live.txt", acl.Download)
	_, s := openStream(t, c, r, nil)
	defer closeStream(t, s)

	// The durable write is the rename-based replace every write to share
	// content goes through, which is the exact shape this stream has to
	// survive.
	if _, err := r.Root().WriteDurable(r.Path(), vfs.DurableOpts{Mode: 0o644}, func(f *vfs.File) error {
		_, werr := f.WriteAt([]byte("replaced content"), 0)
		return werr
	}); err != nil {
		t.Fatalf("replacing the file durably: %v", err)
	}

	if got := drain(t, s); got != "original content" {
		t.Fatalf("the stream produced %q after an atomic replace", got)
	}
}

func TestOpenStreamRefusesADirectoryAndAMissingDownloadBit(t *testing.T) {
	c, st, host, _ := listable(t)
	writeFile(t, host, "a.txt", "x")
	if err := os.Mkdir(filepath.Join(host, "sub"), 0o755); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}

	dir := resolveAt(t, c, "Documents/sub", acl.Download)
	if _, _, err := c.OpenStream(context.Background(), dir, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("streaming a directory = %v, want ErrDenied", err)
	}

	seedUser(t, st, 2, "bob")
	grantRead(t, c, st, 2, 10, "Documents")
	readOnly, err := c.Resolve(2, vpath(t, "Documents/a.txt"), acl.Read)
	if err != nil {
		t.Fatalf("resolving as the read-only user: %v", err)
	}
	if _, _, oerr := c.OpenStream(context.Background(), readOnly, nil); !errors.Is(oerr, ErrDenied) {
		t.Fatalf("streaming without Download = %v, want ErrDenied", oerr)
	}
}

func TestOpenRandomReadsTheTailAndNeedsBothBits(t *testing.T) {
	c, st, host, _ := listable(t)
	writeFile(t, host, "container.bin", "headerBODYtrailer")
	r := resolveAt(t, c, "Documents/container.bin", acl.Read|acl.Download)

	entry, rr, err := c.OpenRandom(context.Background(), r)
	if err != nil {
		t.Fatalf("OpenRandom: %v", err)
	}
	defer func() {
		if cerr := rr.Close(); cerr != nil {
			t.Errorf("closing the random reader: %v", cerr)
		}
	}()
	if rr.Size != 17 || entry.Size != 17 {
		t.Fatalf("the reader reports size %d and the entry %d, want 17", rr.Size, entry.Size)
	}
	// The central-directory access pattern: seek to the tail without having
	// read anything before it.
	tail := make([]byte, 7)
	if n, rerr := rr.ReadAt(tail, rr.Size-7); n != 7 || rerr != nil {
		t.Fatalf("reading the tail = %d, %v", n, rerr)
	}
	if string(tail) != "trailer" {
		t.Fatalf("the tail read produced %q", tail)
	}

	// Read alone and Download alone each miss a bit the whole-file random
	// reader needs.
	seedUser(t, st, 2, "bob")
	grantRead(t, c, st, 2, 10, "Documents")
	readOnly, err := c.Resolve(2, vpath(t, "Documents/container.bin"), acl.Read)
	if err != nil {
		t.Fatalf("resolving as the read-only user: %v", err)
	}
	if _, _, oerr := c.OpenRandom(context.Background(), readOnly); !errors.Is(oerr, ErrDenied) {
		t.Fatalf("OpenRandom without Download = %v, want ErrDenied", oerr)
	}

	if err := os.Mkdir(filepath.Join(host, "sub"), 0o755); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	dir := resolveAt(t, c, "Documents/sub", acl.Read|acl.Download)
	if _, _, oerr := c.OpenRandom(context.Background(), dir); !errors.Is(oerr, ErrDenied) {
		t.Fatalf("OpenRandom on a directory = %v, want ErrDenied", oerr)
	}
}

// walkCollector records the walk and asserts the one-open-descriptor bound
// as it goes.
type walkCollector struct {
	t    *testing.T
	rows []WalkEntry
	body map[string]string
	open int
}

func newCollector(t *testing.T) *walkCollector {
	return &walkCollector{t: t, body: map[string]string{}}
}

func (w *walkCollector) visit(e WalkEntry, s *Stream) error {
	w.rows = append(w.rows, e)
	if s == nil {
		return nil
	}
	w.open++
	if w.open > 1 {
		w.t.Fatalf("the walk held %d streams open at once", w.open)
	}
	b, err := io.ReadAll(s)
	if err != nil {
		return err
	}
	w.body[e.RelPath] = string(b)
	w.open--
	return nil
}

func (w *walkCollector) paths() []string {
	out := make([]string, 0, len(w.rows))
	for _, e := range w.rows {
		out = append(out, e.RelPath)
	}
	return out
}

func TestArchiveWalkCoversATreeUnderTheRootsLeafName(t *testing.T) {
	c, _, host, _ := listable(t)
	if err := os.MkdirAll(filepath.Join(host, "box", "inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, host, "box/top.txt", "top")
	writeFile(t, host, "box/inner/deep.txt", "deep")

	r := resolveAt(t, c, "Documents/box", acl.Read|acl.Download)
	w := newCollector(t)
	if err := c.ArchiveWalk(context.Background(), r, w.visit); err != nil {
		t.Fatalf("ArchiveWalk: %v", err)
	}

	// "box" itself is in the set: the walk announces its own root when that
	// root is a directory. Without it an archive of an empty directory holds
	// nothing, and extracting one loses the directory the caller asked for.
	want := map[string]bool{
		"box": true, "box/top.txt": true,
		"box/inner": true, "box/inner/deep.txt": true,
	}
	for _, p := range w.paths() {
		if !want[p] {
			t.Fatalf("the walk produced the unexpected path %q (all: %v)", p, w.paths())
		}
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("the walk missed %v", want)
	}
	if w.body["box/top.txt"] != "top" || w.body["box/inner/deep.txt"] != "deep" {
		t.Fatalf("the walk produced the bodies %v", w.body)
	}
	for _, e := range w.rows {
		switch e.RelPath {
		case "box/inner":
			if !e.IsDir || !e.Readable {
				t.Fatalf("the directory row is %+v", e)
			}
		case "box/top.txt":
			if e.IsDir || !e.Readable || e.Size != 3 || e.MTimeNs == 0 {
				t.Fatalf("the file row is %+v", e)
			}
		}
	}
}

func TestArchiveWalkStopsAtASubtreeTheCallerCannotRead(t *testing.T) {
	c, st, host, _ := listable(t)
	if err := os.MkdirAll(filepath.Join(host, "closed"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, host, "open.txt", "visible")
	writeFile(t, host, "closed/secret.txt", "hidden")
	denyReadAt(t, c, st, 1, 10, "closed", acl.Download)

	r := resolveAt(t, c, "Documents", acl.Read|acl.Download)
	w := newCollector(t)
	if err := c.ArchiveWalk(context.Background(), r, w.visit); err != nil {
		t.Fatalf("ArchiveWalk: %v", err)
	}

	for _, p := range w.paths() {
		if strings.HasPrefix(p, "closed/") {
			t.Fatalf("the walk descended into an unreadable subtree: %v", w.paths())
		}
	}
	var sawClosed bool
	for _, e := range w.rows {
		if e.RelPath == "closed" {
			sawClosed = e.IsDir
		}
	}
	if !sawClosed {
		t.Fatalf("the unreadable directory did not appear as its own row: %v", w.paths())
	}
	// A walk rooted at the share root has no leaf name, so nothing is
	// prefixed.
	if w.body["open.txt"] != "visible" {
		t.Fatalf("the readable file's body is %q", w.body["open.txt"])
	}
}

func TestArchiveWalkReportsAnUnreadableFileAsSkipped(t *testing.T) {
	c, st, host, _ := listable(t)
	writeFile(t, host, "open.txt", "visible")
	writeFile(t, host, "shut.txt", "hidden")
	denyReadAt(t, c, st, 1, 10, "shut.txt", acl.Download)

	r := resolveAt(t, c, "Documents", acl.Read|acl.Download)
	w := newCollector(t)
	if err := c.ArchiveWalk(context.Background(), r, w.visit); err != nil {
		t.Fatalf("ArchiveWalk: %v", err)
	}

	var skipped bool
	for _, e := range w.rows {
		if e.RelPath == "shut.txt" {
			skipped = !e.Readable
		}
	}
	if !skipped {
		t.Fatalf("the unreadable file is not marked skipped: %+v", w.rows)
	}
	if _, ok := w.body["shut.txt"]; ok {
		t.Fatal("the unreadable file came with a stream")
	}
	if w.body["open.txt"] != "visible" {
		t.Fatal("the walk did not continue past the skipped file")
	}
}

// TestArchiveWalkSkipsAFileThatVanishesBeforeItsOpen uses a dangling symlink,
// whose open fails exactly as a deleted file's does.
func TestArchiveWalkSkipsAFileThatVanishesBeforeItsOpen(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "present.txt", "here")
	if err := os.Symlink("nothing-here", filepath.Join(host, "gone.txt")); err != nil {
		t.Fatalf("creating the dangling symlink: %v", err)
	}

	r := resolveAt(t, c, "Documents", acl.Read|acl.Download)
	w := newCollector(t)
	if err := c.ArchiveWalk(context.Background(), r, w.visit); err != nil {
		t.Fatalf("ArchiveWalk: %v", err)
	}
	if w.body["present.txt"] != "here" {
		t.Fatal("the walk did not survive the vanished entry")
	}
	for _, e := range w.rows {
		if e.RelPath == "gone.txt" && e.Readable {
			t.Fatal("a file whose stat fails was reported readable")
		}
	}
}

func TestAVisitorErrorAbortsTheWalkUnchanged(t *testing.T) {
	c, _, host, _ := listable(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeFile(t, host, name, "x")
	}
	r := resolveAt(t, c, "Documents", acl.Read|acl.Download)

	stop := errors.New("the client hung up")
	seen := 0
	err := c.ArchiveWalk(context.Background(), r, func(WalkEntry, *Stream) error {
		seen++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("ArchiveWalk = %v, want the visitor's own error", err)
	}
	if seen != 1 {
		t.Fatalf("the walk visited %d entries after the abort", seen)
	}
}

func TestASingleFileRootIsAOneEntryWalkAndClosesItsDescriptor(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "alone.txt", "solo")
	r := resolveAt(t, c, "Documents/alone.txt", acl.Read|acl.Download)

	before := openDescriptors(t)
	w := newCollector(t)
	if err := c.ArchiveWalk(context.Background(), r, w.visit); err != nil {
		t.Fatalf("ArchiveWalk: %v", err)
	}
	if len(w.rows) != 1 || w.rows[0].RelPath != "alone.txt" || !w.rows[0].Readable {
		t.Fatalf("the single-file walk produced %+v", w.rows)
	}
	if w.body["alone.txt"] != "solo" {
		t.Fatalf("the single-file walk produced the body %q", w.body["alone.txt"])
	}
	if after := openDescriptors(t); after > before {
		t.Fatalf("the walk leaked a descriptor: %d open before, %d after", before, after)
	}
}

func TestArchiveWalkRefusesWithoutReadAndFailsAFileRootItCannotOpen(t *testing.T) {
	c, st, host, _ := listable(t)
	writeFile(t, host, "a.txt", "x")
	if err := os.Mkdir(filepath.Join(host, "drop"), 0o755); err != nil {
		t.Fatalf("creating the drop directory: %v", err)
	}
	denyReadAt(t, c, st, 1, 10, "drop", acl.Download)
	drop := resolveAt(t, c, "Documents/drop", acl.Download)
	if werr := c.ArchiveWalk(context.Background(), drop, func(WalkEntry, *Stream) error {
		t.Fatal("the visitor ran without the read bit")
		return nil
	}); !errors.Is(werr, ErrDenied) {
		t.Fatalf("ArchiveWalk without Read = %v, want ErrDenied", werr)
	}

	// A file root is the whole archive, so a caller who cannot take its
	// bytes gets a refusal rather than an archive of nothing.
	seedUser(t, st, 2, "bob")
	grantRead(t, c, st, 2, 10, "Documents")
	noDownload, err := c.Resolve(2, vpath(t, "Documents/a.txt"), acl.Read)
	if err != nil {
		t.Fatalf("resolving as the read-only user: %v", err)
	}
	if werr := c.ArchiveWalk(context.Background(), noDownload, func(WalkEntry, *Stream) error {
		return nil
	}); !errors.Is(werr, ErrDenied) {
		t.Fatalf("ArchiveWalk over an unopenable file root = %v, want ErrDenied", werr)
	}
}

func TestSatAddSaturates(t *testing.T) {
	if got := satAdd(0); got != 1 {
		t.Fatalf("satAdd(0) = %d, want 1", got)
	}
	if got := satAdd(^uint64(0)); got != ^uint64(0) {
		t.Fatalf("satAdd(max) = %d, want the maximum unchanged", got)
	}
}

func TestStreamReadOfAnEmptyBufferIsNotAnEnd(t *testing.T) {
	c, _, host, _ := listable(t)
	writeFile(t, host, "some.txt", "abc")
	r := resolveAt(t, c, "Documents/some.txt", acl.Download)
	_, s := openStream(t, c, r, nil)
	defer closeStream(t, s)

	n, err := s.Read(nil)
	if n != 0 || err != nil {
		t.Fatalf("a zero-length read = %d, %v, want 0 and no error", n, err)
	}
	if s.Remaining() != 3 {
		t.Fatalf("a zero-length read consumed the stream: %d left", s.Remaining())
	}
	if got := drain(t, s); got != "abc" {
		t.Fatalf("the stream then produced %q", got)
	}
}

func closeStream(t *testing.T, s *Stream) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("closing the stream: %v", err)
	}
}
