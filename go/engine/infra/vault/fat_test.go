package vault

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
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
	size := mustNarrow[uint64](sizeBytes, "test volume size")
	if err := Format(dev, size); err != nil {
		t.Fatalf("Format: %v", err)
	}
	fsys, err := Mount(dev, size, clock.Fixed(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
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
	if rerr := fsys.ReadFile(p, &buf); rerr != nil {
		t.Fatalf("ReadFile: %v", rerr)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("read back %d bytes, want %d, mismatch", buf.Len(), len(content))
	}
	st, serr := fsys.Stat(p)
	if serr != nil {
		t.Fatalf("Stat: %v", serr)
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
	st, serr := fsys.Stat(p)
	if serr != nil {
		t.Fatalf("Stat: %v", serr)
	}
	if st.Size != 100 {
		t.Fatalf("size after shrink = %d, want 100", st.Size)
	}
	var buf bytes.Buffer
	if rerr := fsys.ReadFile(p, &buf); rerr != nil {
		t.Fatalf("ReadFile after shrink: %v", rerr)
	}
	if !bytes.Equal(buf.Bytes(), content[:100]) {
		t.Fatalf("content after shrink mismatch")
	}

	if terr := fsys.Truncate(p, 5000); terr != nil {
		t.Fatalf("Truncate longer: %v", terr)
	}
	st, serr = fsys.Stat(p)
	if serr != nil {
		t.Fatalf("Stat: %v", serr)
	}
	if st.Size != 5000 {
		t.Fatalf("size after grow = %d, want 5000", st.Size)
	}
	buf.Reset()
	if rerr := fsys.ReadFile(p, &buf); rerr != nil {
		t.Fatalf("ReadFile after grow: %v", rerr)
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
	if rerr := fsys.Rename(from, to, false); rerr != nil {
		t.Fatalf("Rename: %v", rerr)
	}
	after, err := fsys.Stat(to)
	if err != nil {
		t.Fatalf("Stat after rename: %v", err)
	}
	if after.Ino != before.Ino {
		t.Fatalf("inode changed across rename: before=%d after=%d", before.Ino, after.Ino)
	}
}

// buildFAT1216Image lays out a minimal, self-consistent FAT12 or FAT16 boot
// sector, FAT tables and fixed root region directly onto dev. Format only
// ever writes FAT32, so exercising the other two widths means constructing
// the geometry by hand instead of round-tripping through this driver's own
// writer: the fixed point search for fatSize mirrors computeFATSize's own
// approach, just against the FAT12/FAT16 entry width instead of FAT32's.
func buildFAT1216Image(t *testing.T, dev Device, sizeBytes int64, kind fatKind, rootEntryCount uint32) {
	t.Helper()
	const bps = 512
	const reserved = 1
	const numFATs = 2
	const spc = 1

	entriesPerSector := uint32(bps / 2)
	if kind == fat12 {
		entriesPerSector = bps * 2 / 3
	}
	totalSectors := mustNarrow[uint32](sizeBytes/bps, "test image sectors")
	rootDirSectors := (rootEntryCount*32 + bps - 1) / bps

	fatSize := uint32(1)
	var totalClusters uint32
	for {
		dataSectors := totalSectors - reserved - numFATs*fatSize - rootDirSectors
		totalClusters = dataSectors / spc
		needed := (totalClusters + 2 + entriesPerSector - 1) / entriesPerSector
		if needed <= fatSize {
			break
		}
		fatSize = needed
	}

	sector := make([]byte, bps)
	sector[0], sector[1], sector[2] = 0xEB, 0x3C, 0x90
	copy(sector[3:11], "STOWTEST")
	binary.LittleEndian.PutUint16(sector[11:13], bps)
	sector[13] = spc
	binary.LittleEndian.PutUint16(sector[14:16], reserved)
	sector[16] = numFATs
	binary.LittleEndian.PutUint16(sector[17:19], mustNarrow[uint16](rootEntryCount, "test root entry count"))
	binary.LittleEndian.PutUint16(sector[19:21], mustNarrow[uint16](totalSectors, "test total sectors"))
	sector[21] = 0xF8
	binary.LittleEndian.PutUint16(sector[22:24], mustNarrow[uint16](fatSize, "test FAT size"))
	binary.LittleEndian.PutUint16(sector[24:26], 0x3F)
	binary.LittleEndian.PutUint16(sector[26:28], 0xFF)
	sector[36] = 0x80
	sector[38] = 0x29
	binary.LittleEndian.PutUint32(sector[39:43], 0xC0FFEE)
	copy(sector[43:54], "NO NAME    ")
	if kind == fat12 {
		copy(sector[54:62], "FAT12   ")
	} else {
		copy(sector[54:62], "FAT16   ")
	}
	sector[510], sector[511] = 0x55, 0xAA

	if _, err := dev.WriteAt(sector, 0); err != nil {
		t.Fatalf("write boot sector: %v", err)
	}
}

// newFAT1216TestFS builds and mounts a hand-built FAT12 or FAT16 image of
// sizeBytes with rootEntryCount slots in its fixed root directory, and
// confirms this driver classified it the same way it was built.
func newFAT1216TestFS(t *testing.T, kind fatKind, sizeBytes int64, rootEntryCount uint32) *FS {
	t.Helper()
	dev := newMemDevice(sizeBytes)
	buildFAT1216Image(t, dev, sizeBytes, kind, rootEntryCount)
	fsys, err := Mount(dev, mustNarrow[uint64](sizeBytes, "test volume size"), clock.Fixed(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if fsys.bpb.kind != kind {
		t.Fatalf("Mount classified the image as kind %v, want %v", fsys.bpb.kind, kind)
	}
	return fsys
}

// testFAT1216ReadWriteDeleteExtend covers a FAT12 or FAT16 volume's read and
// write path end to end: a directory listing, a file's bytes, growing an
// existing file's chain across a cluster boundary, and deleting it.
func testFAT1216ReadWriteDeleteExtend(t *testing.T, kind fatKind, sizeBytes int64, rootEntryCount uint32) {
	t.Helper()
	fsys := newFAT1216TestFS(t, kind, sizeBytes, rootEntryCount)
	root := mustPath(t, "")

	entries, derr := fsys.ReadDir(root)
	if derr != nil {
		t.Fatalf("ReadDir empty root: %v", derr)
	}
	if len(entries) != 0 {
		t.Fatalf("fresh root has %d entries, want 0", len(entries))
	}

	p := mustPath(t, "hello.txt")
	if cerr := fsys.CreateFile(p); cerr != nil {
		t.Fatalf("CreateFile: %v", cerr)
	}
	small := bytes.Repeat([]byte{0xAB}, 100) // well under one 512B cluster
	if _, werr := fsys.WriteFileStaged(p, bytes.NewReader(small), false, 0); werr != nil {
		t.Fatalf("WriteFileStaged: %v", werr)
	}

	entries, derr = fsys.ReadDir(root)
	if derr != nil {
		t.Fatalf("ReadDir after create: %v", derr)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" || entries[0].IsDir {
		t.Fatalf("ReadDir = %+v, want one file named hello.txt", entries)
	}

	var buf bytes.Buffer
	if rerr := fsys.ReadFile(p, &buf); rerr != nil {
		t.Fatalf("ReadFile: %v", rerr)
	}
	if !bytes.Equal(buf.Bytes(), small) {
		t.Fatalf("read back %d bytes, want %d matching bytes", buf.Len(), len(small))
	}

	// Grow the same file past a cluster boundary: this exercises
	// ensureExtent's chain growth path on an already allocated chain, not
	// just a bigger fresh write.
	const grownSize = 1500 // spans 3 clusters at 512B/cluster
	if terr := fsys.Truncate(p, grownSize); terr != nil {
		t.Fatalf("Truncate grow: %v", terr)
	}
	st, serr := fsys.Stat(p)
	if serr != nil {
		t.Fatalf("Stat after grow: %v", serr)
	}
	if st.Size != grownSize {
		t.Fatalf("size after grow = %d, want %d", st.Size, grownSize)
	}
	buf.Reset()
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile after grow: %v", err)
	}
	grown := buf.Bytes()
	if !bytes.Equal(grown[:len(small)], small) {
		t.Fatalf("original bytes changed after growing across a cluster boundary")
	}
	for i, b := range grown[len(small):] {
		if b != 0 {
			t.Fatalf("byte %d after grow = %#x, want zero fill", len(small)+i, b)
		}
	}

	if err := fsys.Remove(p); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, err := fsys.findEntry(fsys.rootDirStart(), "hello.txt"); err != nil {
		t.Fatalf("findEntry after delete: %v", err)
	} else if ok {
		t.Fatalf("hello.txt still found after delete")
	}
	entries, derr = fsys.ReadDir(root)
	if derr != nil {
		t.Fatalf("ReadDir after delete: %v", derr)
	}
	if len(entries) != 0 {
		t.Fatalf("root has %d entries after delete, want 0", len(entries))
	}
}

func TestFAT12ReadWriteDeleteExtend(t *testing.T) {
	testFAT1216ReadWriteDeleteExtend(t, fat12, 1<<20, 224)
}

func TestFAT16ReadWriteDeleteExtend(t *testing.T) {
	testFAT1216ReadWriteDeleteExtend(t, fat16, 16<<20, 512)
}

// testFAT1216FixedRootFillsUp fills a FAT12/FAT16 fixed root directory to
// its slot limit and confirms the next create is refused cleanly, the same
// error a full disk gives, rather than spilling entries into the data area
// that starts immediately after the fixed root region.
func testFAT1216FixedRootFillsUp(t *testing.T, kind fatKind) {
	t.Helper()
	// A one sector (16 slot) root fills after only a handful of short,
	// already 8.3-compatible names, keeping the test fast.
	const rootEntryCount = 16
	sizeBytes := int64(1 << 20)
	if kind == fat16 {
		sizeBytes = 16 << 20
	}
	fsys := newFAT1216TestFS(t, kind, sizeBytes, rootEntryCount)

	created := 0
	var lastErr error
	for i := range rootEntryCount {
		name := fmt.Sprintf("f%d.txt", i)
		if err := fsys.CreateFile(mustPath(t, name)); err != nil {
			lastErr = err
			break
		}
		created++
	}
	if lastErr == nil {
		t.Fatalf("filled the fixed root without ever running out of room (created %d entries)", created)
	}
	if !errors.Is(lastErr, ErrNoSpaceOnVolume) {
		t.Fatalf("create after the root filled up: got %v, want ErrNoSpaceOnVolume", lastErr)
	}

	// The region itself is intact: every entry created before the
	// refusal is still there, in a clean listing, with nothing left over
	// from a partially written batch.
	entries, err := fsys.ReadDir(mustPath(t, ""))
	if err != nil {
		t.Fatalf("ReadDir after filling the root: %v", err)
	}
	if len(entries) != created {
		t.Fatalf("ReadDir reports %d entries, want the %d that were actually created", len(entries), created)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name] = true
	}
	for i := range created {
		name := fmt.Sprintf("f%d.txt", i)
		if !seen[name] {
			t.Fatalf("entry %q missing from a full root's listing", name)
		}
	}
}

func TestFAT12FixedRootFillsUp(t *testing.T) { testFAT1216FixedRootFillsUp(t, fat12) }

func TestFAT16FixedRootFillsUp(t *testing.T) { testFAT1216FixedRootFillsUp(t, fat16) }

// testRealFormatterImage formats an image with the real mkfs.vfat, the only
// thing that proves this driver's BPB parse against a formatter other than
// its own writer, then, when mtools is also available, writes a file into
// it with mcopy and reads it back through this driver.
func testRealFormatterImage(t *testing.T, wantKind fatKind, fatBits int, blocks int) {
	mkfsPath, err := exec.LookPath("mkfs.vfat")
	if err != nil {
		t.Skip("mkfs.vfat not available")
	}

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "real.img")
	if out, cerr := mkfsVfat(mkfsPath, strconv.Itoa(fatBits), imgPath, strconv.Itoa(blocks)); cerr != nil {
		t.Fatalf("mkfs.vfat: %v\n%s", cerr, out)
	}

	content := []byte("real formatter round trip\n")
	mcopyPath, mcopyErr := exec.LookPath("mcopy")
	if mcopyErr == nil {
		srcPath := filepath.Join(dir, "hello.txt")
		if werr := os.WriteFile(srcPath, content, 0o600); werr != nil {
			t.Fatalf("write source file: %v", werr)
		}
		if out, cerr := mcopyInto(mcopyPath, imgPath, srcPath); cerr != nil {
			t.Fatalf("mcopy: %v\n%s", cerr, out)
		}
	} else {
		t.Log("mtools not available, only verifying the BPB parse and cluster count")
	}

	raw, rerr := os.ReadFile(imgPath)
	if rerr != nil {
		t.Fatalf("read formatted image: %v", rerr)
	}
	dev := newMemDevice(int64(len(raw)))
	if _, werr := dev.WriteAt(raw, 0); werr != nil {
		t.Fatalf("load image into device: %v", werr)
	}

	fsys, merr := Mount(dev, mustNarrow[uint64](len(raw), "real image size"), clock.Fixed(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	if merr != nil {
		t.Fatalf("Mount a real mkfs.vfat -F %d image: %v", fatBits, merr)
	}
	if fsys.bpb.kind != wantKind {
		t.Fatalf("Mount classified a real mkfs.vfat -F %d image as kind %v, want %v", fatBits, fsys.bpb.kind, wantKind)
	}
	if fsys.totalClusters == 0 {
		t.Fatalf("Mount reported zero data clusters for a real image")
	}

	if mcopyErr == nil {
		var buf bytes.Buffer
		if ferr := fsys.ReadFile(mustPath(t, "hello.txt"), &buf); ferr != nil {
			t.Fatalf("ReadFile a real image's mtools-written file: %v", ferr)
		}
		if !bytes.Equal(buf.Bytes(), content) {
			t.Fatalf("content mismatch reading mtools-written file: got %q, want %q", buf.Bytes(), content)
		}
	}
}

func TestFAT12RealFormatterImage(t *testing.T) {
	testRealFormatterImage(t, fat12, 12, 1024) // 1024 * 1024 bytes = 1 MiB
}

func TestFAT16RealFormatterImage(t *testing.T) {
	testRealFormatterImage(t, fat16, 16, 16384) // 16384 * 1024 bytes = 16 MiB
}

// mkfsVfat and mcopyInto launch the host's own FAT tools. The tool path is
// a function parameter, so it names what exec.LookPath resolved at the
// point exec.Command reads it, and the paths are under t.TempDir.
func mkfsVfat(tool, bits, imgPath, blocks string) ([]byte, error) {
	return exec.Command(tool, "-F", bits, "-C", imgPath, blocks).CombinedOutput()
}

func mcopyInto(tool, imgPath, srcPath string) ([]byte, error) {
	cmd := exec.Command(tool, "-i", imgPath, srcPath, "::hello.txt")
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	return cmd.CombinedOutput()
}
