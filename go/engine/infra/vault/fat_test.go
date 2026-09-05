package vault

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// memDevice is an in-memory Device, so the FAT driver is testable without a
// real container or even a real file underneath it.
type memDevice struct {
	mu   sync.Mutex
	data []byte
}

func newMemDevice(size int64) *memDevice { return &memDevice{data: make([]byte, size)} }

func (d *memDevice) ReadAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if off < 0 || off >= int64(len(d.data)) {
		return 0, io.EOF
	}
	n := copy(p, d.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (d *memDevice) WriteAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	end := off + int64(len(p))
	if off < 0 || end > int64(len(d.data)) {
		return 0, io.ErrShortWrite
	}
	copy(d.data[off:end], p)
	return len(p), nil
}

func mustPath(t *testing.T, s string) vfs.SafePath {
	t.Helper()
	p, err := vfs.ParseSafePath(s)
	if err != nil {
		t.Fatalf("ParseSafePath(%q): %v", s, err)
	}
	return p
}

func newTestFS(t *testing.T, sizeBytes int64) (*FS, Device) {
	t.Helper()
	dev := newMemDevice(sizeBytes)
	if err := Format(dev, uint64(sizeBytes)); err != nil {
		t.Fatalf("Format: %v", err)
	}
	fsys, err := Mount(dev, uint64(sizeBytes))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return fsys, dev
}

// A modest 64 MiB volume keeps 512-byte clusters, well above the 65525
// cluster threshold real FAT32 tooling uses to recognize the format, so a
// test volume looks like a genuine FAT32 filesystem, not just one this
// driver alone would accept.
const testVolumeSize = 64 << 20

func TestFormatAndMount(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	total, free := fsys.Space()
	if total == 0 {
		t.Fatalf("total space is zero")
	}
	if free == 0 || free > total {
		t.Fatalf("free space %d out of range for total %d", free, total)
	}
	if err := fsys.Alive(); err != nil {
		t.Fatalf("Alive: %v", err)
	}
}

func TestCreateFileAndReadBack(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	p := mustPath(t, "hello.txt")
	if err := fsys.CreateFile(p); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	content := bytes.Repeat([]byte("stowcloud vault "), 1000) // spans several clusters at 512B/cluster
	if _, err := fsys.WriteFileStaged(p, bytes.NewReader(content), false, 0); err != nil {
		t.Fatalf("WriteFileStaged: %v", err)
	}
	var buf bytes.Buffer
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("read back %d bytes, want %d, mismatch", buf.Len(), len(content))
	}
	st, err := fsys.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size != uint64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", st.Size, len(content))
	}
}

func TestNestedDirectories(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	a := mustPath(t, "a")
	ab := mustPath(t, "a/b")
	abFile := mustPath(t, "a/b/c.txt")
	if err := fsys.Mkdir(a); err != nil {
		t.Fatalf("Mkdir a: %v", err)
	}
	if err := fsys.Mkdir(ab); err != nil {
		t.Fatalf("Mkdir a/b: %v", err)
	}
	if err := fsys.CreateFile(abFile); err != nil {
		t.Fatalf("CreateFile a/b/c.txt: %v", err)
	}
	st, err := fsys.Stat(abFile)
	if err != nil {
		t.Fatalf("Stat a/b/c.txt: %v", err)
	}
	if st.IsDir {
		t.Fatalf("a/b/c.txt reported as a directory")
	}
	entries, err := fsys.ReadDir(ab)
	if err != nil {
		t.Fatalf("ReadDir a/b: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "c.txt" {
		t.Fatalf("ReadDir a/b = %+v, want exactly c.txt", entries)
	}
}

func TestTruncateShorterAndLonger(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	p := mustPath(t, "grow.bin")
	if err := fsys.CreateFile(p); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	content := bytes.Repeat([]byte{0xAB}, 10000)
	if _, err := fsys.WriteFileStaged(p, bytes.NewReader(content), false, 0); err != nil {
		t.Fatalf("WriteFileStaged: %v", err)
	}

	if err := fsys.Truncate(p, 100); err != nil {
		t.Fatalf("Truncate shorter: %v", err)
	}
	st, err := fsys.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size != 100 {
		t.Fatalf("size after shrink = %d, want 100", st.Size)
	}
	var buf bytes.Buffer
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile after shrink: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content[:100]) {
		t.Fatalf("content after shrink mismatch")
	}

	if err := fsys.Truncate(p, 5000); err != nil {
		t.Fatalf("Truncate longer: %v", err)
	}
	st, err = fsys.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size != 5000 {
		t.Fatalf("size after grow = %d, want 5000", st.Size)
	}
	buf.Reset()
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile after grow: %v", err)
	}
	grown := buf.Bytes()
	if !bytes.Equal(grown[:100], content[:100]) {
		t.Fatalf("original bytes changed after grow")
	}
	for i, b := range grown[100:] {
		if b != 0 {
			t.Fatalf("byte %d after grow = %#x, want zero fill", 100+i, b)
		}
	}
}

func TestDeleteFreesClusters(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	p := mustPath(t, "big.bin")
	if err := fsys.CreateFile(p); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	content := bytes.Repeat([]byte{0x42}, 200000)
	_, before := fsys.Space()

	if _, err := fsys.WriteFileStaged(p, bytes.NewReader(content), false, 0); err != nil {
		t.Fatalf("WriteFileStaged: %v", err)
	}
	_, afterWrite := fsys.Space()
	if afterWrite >= before {
		t.Fatalf("free space did not shrink after writing: before=%d after=%d", before, afterWrite)
	}

	if err := fsys.Remove(p); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, afterDelete := fsys.Space()
	if afterDelete != before {
		t.Fatalf("free space after delete = %d, want back to %d", afterDelete, before)
	}
	if _, ok, err := fsys.findEntry(fsys.bpb.rootCluster, "big.bin"); err != nil {
		t.Fatalf("findEntry after delete: %v", err)
	} else if ok {
		t.Fatalf("big.bin still found after delete")
	}
}

func TestRenameAcrossDirectories(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	srcDir := mustPath(t, "src")
	dstDir := mustPath(t, "dst")
	if err := fsys.Mkdir(srcDir); err != nil {
		t.Fatalf("Mkdir src: %v", err)
	}
	if err := fsys.Mkdir(dstDir); err != nil {
		t.Fatalf("Mkdir dst: %v", err)
	}
	from := mustPath(t, "src/file.txt")
	to := mustPath(t, "dst/moved.txt")
	if err := fsys.CreateFile(from); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	content := []byte("cross directory rename")
	if _, err := fsys.WriteFileStaged(from, bytes.NewReader(content), false, 0); err != nil {
		t.Fatalf("WriteFileStaged: %v", err)
	}
	if err := fsys.Rename(from, to, false); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := fsys.Stat(from); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(from) after rename = %v, want ErrNotFound", err)
	}
	var buf bytes.Buffer
	if err := fsys.ReadFile(to, &buf); err != nil {
		t.Fatalf("ReadFile(to): %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("content after rename mismatch")
	}
}

func TestRmdirRefusesNonEmpty(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	dir := mustPath(t, "occupied")
	if err := fsys.Mkdir(dir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := fsys.CreateFile(mustPath(t, "occupied/x.txt")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := fsys.Rmdir(dir); !errors.Is(err, vfs.ErrNotEmpty) {
		t.Fatalf("Rmdir non-empty = %v, want ErrNotEmpty", err)
	}
	if err := fsys.Remove(mustPath(t, "occupied/x.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := fsys.Rmdir(dir); err != nil {
		t.Fatalf("Rmdir empty: %v", err)
	}
}

func TestLongFileNameRoundTrip(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	names := []string{
		"a fairly long file name with spaces.txt",
		"\u65e5\u672c\u8a9e\u30d5\u30a1\u30a4\u30eb.txt", // Japanese, non-ASCII
		"caf\u00e9 menu.txt",                             // Latin-1 supplement accent
	}
	for _, name := range names {
		p := mustPath(t, name)
		if err := fsys.CreateFile(p); err != nil {
			t.Fatalf("CreateFile(%q): %v", name, err)
		}
	}
	entries, err := fsys.ReadDir(vfs.RootPath())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name] = true
	}
	for _, name := range names {
		if !got[name] {
			t.Errorf("round-tripped listing missing %q, got %+v", name, entries)
		}
	}
}

func TestRefusesUnencodableName(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	bad := []string{"has*star.txt", "has?question.txt", "pipe|char.txt", string(rune(0x1F600)) + ".txt"}
	for _, name := range bad {
		p, err := vfs.ParseSafePath(name)
		if err != nil {
			// vfs itself already refused it (e.g. it also dislikes the name);
			// that still proves the name never reaches the FAT layer.
			continue
		}
		if err := fsys.CreateFile(p); !errors.Is(err, vfs.ErrInvalidName) {
			t.Errorf("CreateFile(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestShortNameUniqueness(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	for i := range 12 {
		name := "REPORT VERSION " + string(rune('A'+i)) + ".TXT"
		if err := fsys.CreateFile(mustPath(t, name)); err != nil {
			t.Fatalf("CreateFile(%q): %v", name, err)
		}
	}
	entries, err := fsys.ReadDir(vfs.RootPath())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 12 {
		t.Fatalf("got %d entries, want 12", len(entries))
	}
	seen := map[string]bool{}
	err = fsys.scanRawEntries(fsys.bpb.rootCluster, func(e rawDirEntry, off int64) bool {
		if e[0] == direntFree {
			return true
		}
		if e[0] == direntDeleted || e[11] == attrLongName {
			return false
		}
		var key [11]byte
		copy(key[:], e[0:11])
		name := string(key[:])
		if seen[name] {
			t.Errorf("duplicate short name %q", name)
		}
		seen[name] = true
		return false
	})
	if err != nil {
		t.Fatalf("scanRawEntries: %v", err)
	}
}

// Two empty files sharing one first-cluster value (0, since neither owns
// content) would otherwise collide on the (dev, ino) identity tuple the
// rest of the product keys a link or a cache row on: store/cache's own
// collision test exists because this class of bug already happened once.
func TestEmptyFilesGetDistinctInodes(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	a := mustPath(t, "empty-a.txt")
	b := mustPath(t, "empty-b.txt")
	if err := fsys.CreateFile(a); err != nil {
		t.Fatalf("CreateFile a: %v", err)
	}
	if err := fsys.CreateFile(b); err != nil {
		t.Fatalf("CreateFile b: %v", err)
	}
	statA, err := fsys.Stat(a)
	if err != nil {
		t.Fatalf("Stat a: %v", err)
	}
	statB, err := fsys.Stat(b)
	if err != nil {
		t.Fatalf("Stat b: %v", err)
	}
	if statA.Size != 0 || statB.Size != 0 {
		t.Fatalf("expected both files empty, got sizes %d and %d", statA.Size, statB.Size)
	}
	if statA.Ino == 0 || statB.Ino == 0 {
		t.Fatalf("empty file inode must not be the bare zero cluster: a=%d b=%d", statA.Ino, statB.Ino)
	}
	if statA.Ino == statB.Ino {
		t.Fatalf("two distinct empty files reported the same inode %d", statA.Ino)
	}

	rootStat, err := fsys.Stat(vfs.RootPath())
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if rootStat.Ino == statA.Ino || rootStat.Ino == statB.Ino {
		t.Fatalf("root directory inode %d collides with an empty file's inode", rootStat.Ino)
	}
}

// A non-empty file's inode is its first cluster, which a rename never
// touches, so it must survive the move; an empty file has no such
// guarantee, since it owns no cluster to begin with.
func TestFileKeepsInodeAcrossRename(t *testing.T) {
	fsys, _ := newTestFS(t, testVolumeSize)
	from := mustPath(t, "before.bin")
	to := mustPath(t, "after.bin")
	if err := fsys.CreateFile(from); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	content := bytes.Repeat([]byte("keep the inode stable"), 100)
	if _, err := fsys.WriteFileStaged(from, bytes.NewReader(content), false, 0); err != nil {
		t.Fatalf("WriteFileStaged: %v", err)
	}
	before, err := fsys.Stat(from)
	if err != nil {
		t.Fatalf("Stat before rename: %v", err)
	}
	if before.Ino == 0 {
		t.Fatalf("non-empty file has zero inode")
	}
	if err := fsys.Rename(from, to, false); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	after, err := fsys.Stat(to)
	if err != nil {
		t.Fatalf("Stat after rename: %v", err)
	}
	if after.Ino != before.Ino {
		t.Fatalf("inode changed across rename: before=%d after=%d", before.Ino, after.Ino)
	}
}
