package vault

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// newExFatTestFS formats and mounts a fresh in-memory exFAT volume of
// sizeBytes, the same memDevice fat_test.go uses so both drivers are
// tested without a real container underneath either one.
func newExFatTestFS(t *testing.T, sizeBytes int64) (*ExFatFS, Device) {
	t.Helper()
	dev := newMemDevice(sizeBytes)
	size := mustNarrow[uint64](sizeBytes, "test volume size")
	if err := FormatExFat(dev, size); err != nil {
		t.Fatalf("FormatExFat: %v", err)
	}
	fsys, err := MountExFat(dev, size, clock.Fixed(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("MountExFat: %v", err)
	}
	return fsys, dev
}

// testExFatVolumeSize is small enough to keep every test fast and large
// enough to hold several thousand 4 KiB clusters, well past a single
// cluster boundary.
const testExFatVolumeSize = 8 << 20

func TestExFatFormatAndMount(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
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
	label, err := fsys.Label()
	if err != nil {
		t.Fatalf("Label: %v", err)
	}
	if label != "" {
		t.Fatalf("Label() = %q, want no label on a fresh FormatExFat volume", label)
	}
}

func TestExFatCreateFileAndReadBack(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
	p := mustPath(t, "hello.txt")
	if err := fsys.CreateFile(p); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	content := []byte("stowcloud vault exfat driver")
	if _, err := fsys.WriteFileStaged(p, bytes.NewReader(content), false, 0); err != nil {
		t.Fatalf("WriteFileStaged: %v", err)
	}
	var buf bytes.Buffer
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("read back %q, want %q", buf.Bytes(), content)
	}
	st, err := fsys.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size != uint64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", st.Size, len(content))
	}
	if st.IsDir {
		t.Fatalf("a plain file reported as a directory")
	}
}

func TestExFatListDirectory(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
	if err := fsys.Mkdir(mustPath(t, "a")); err != nil {
		t.Fatalf("Mkdir a: %v", err)
	}
	if err := fsys.CreateFile(mustPath(t, "a/one.txt")); err != nil {
		t.Fatalf("CreateFile a/one.txt: %v", err)
	}
	if err := fsys.CreateFile(mustPath(t, "a/two.txt")); err != nil {
		t.Fatalf("CreateFile a/two.txt: %v", err)
	}
	entries, err := fsys.ReadDir(mustPath(t, "a"))
	if err != nil {
		t.Fatalf("ReadDir a: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name] = true
		if e.IsDir {
			t.Fatalf("entry %q reported as a directory", e.Name)
		}
	}
	if len(entries) != 2 || !got["one.txt"] || !got["two.txt"] {
		t.Fatalf("ReadDir a = %+v, want exactly one.txt and two.txt", entries)
	}

	root, err := fsys.ReadDir(vfs.RootPath())
	if err != nil {
		t.Fatalf("ReadDir root: %v", err)
	}
	if len(root) != 1 || root[0].Name != "a" || !root[0].IsDir {
		t.Fatalf("ReadDir root = %+v, want exactly directory a", root)
	}
}

// TestExFatExtendAcrossClusterBoundary writes content spanning several
// clusters in one WriteFileStaged call, exercising ensureExtent's chain
// growth and readExtent's multi-cluster stitching together.
func TestExFatExtendAcrossClusterBoundary(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
	p := mustPath(t, "spanning.bin")
	if err := fsys.CreateFile(p); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	bpc := int(fsys.bpb.bytesPerCluster())
	content := bytes.Repeat([]byte("exfat-cluster-boundary-"), (bpc*3)/24+10)
	if _, err := fsys.WriteFileStaged(p, bytes.NewReader(content), false, 0); err != nil {
		t.Fatalf("WriteFileStaged: %v", err)
	}
	entry, ok, err := fsys.findEntry(fsys.bpb.rootCluster, false, 0, "spanning.bin")
	if err != nil {
		t.Fatalf("findEntry: %v", err)
	}
	if !ok {
		t.Fatalf("spanning.bin not found after write")
	}
	clusters, err := fsys.chainClusters(entry.firstCluster)
	if err != nil {
		t.Fatalf("chainClusters: %v", err)
	}
	if len(clusters) < 2 {
		t.Fatalf("content of %d bytes at %d bytes/cluster only used %d clusters, want at least 2",
			len(content), bpc, len(clusters))
	}
	var buf bytes.Buffer
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("content mismatch after spanning %d clusters", len(clusters))
	}

	// Truncate shorter then longer again, the other path that has to grow
	// and shrink a chain already spanning more than one cluster.
	if err := fsys.Truncate(p, 10); err != nil {
		t.Fatalf("Truncate shorter: %v", err)
	}
	buf.Reset()
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile after shrink: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content[:10]) {
		t.Fatalf("content after shrink mismatch")
	}
	grownSize := mustNarrow[uint64](bpc, "bytes per cluster")*2 + 500
	if err := fsys.Truncate(p, grownSize); err != nil {
		t.Fatalf("Truncate longer: %v", err)
	}
	buf.Reset()
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("ReadFile after grow: %v", err)
	}
	grown := buf.Bytes()
	if uint64(len(grown)) != grownSize {
		t.Fatalf("size after grow = %d, want %d", len(grown), grownSize)
	}
	if !bytes.Equal(grown[:10], content[:10]) {
		t.Fatalf("original bytes changed after grow")
	}
	for i, b := range grown[10:] {
		if b != 0 {
			t.Fatalf("byte %d after grow = %#x, want zero fill", 10+i, b)
		}
	}
}

func TestExFatDeleteFreesClusters(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
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
	if _, ok, err := fsys.findEntry(fsys.bpb.rootCluster, false, 0, "big.bin"); err != nil {
		t.Fatalf("findEntry after delete: %v", err)
	} else if ok {
		t.Fatalf("big.bin still found after delete")
	}
}

func TestExFatMkdirRmdir(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
	dir := mustPath(t, "occupied")
	if err := fsys.Mkdir(dir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := fsys.Mkdir(dir); !errors.Is(err, vfs.ErrExists) {
		t.Fatalf("Mkdir over an existing directory = %v, want ErrExists", err)
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
	if _, err := fsys.Stat(dir); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat after Rmdir = %v, want ErrNotFound", err)
	}
}

// TestExFatNestedDirectoryGrowth creates enough entries in a subdirectory
// to force its own cluster chain to grow, which exercises syncDirSize:
// the subdirectory's own Stream Extension entry, held in its parent, has
// to track the new chain length or a later listing would stop short.
func TestExFatNestedDirectoryGrowth(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
	sub := mustPath(t, "sub")
	if err := fsys.Mkdir(sub); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	bpc := int(fsys.bpb.bytesPerCluster())
	// Each entry set (a File entry, a Stream Extension entry and one File
	// Name entry) is 96 bytes; comfortably more than one cluster's worth
	// of them forces at least one chain growth.
	count := bpc/96 + 20
	for i := range count {
		name := fmt.Sprintf("sub/f%04d.txt", i)
		if err := fsys.CreateFile(mustPath(t, name)); err != nil {
			t.Fatalf("CreateFile(%q): %v", name, err)
		}
	}
	entries, err := fsys.ReadDir(sub)
	if err != nil {
		t.Fatalf("ReadDir sub: %v", err)
	}
	if len(entries) != count {
		t.Fatalf("ReadDir sub returned %d entries, want %d", len(entries), count)
	}
	subInfo, err := fsys.resolve(sub)
	if err != nil {
		t.Fatalf("resolve sub: %v", err)
	}
	clusters, err := fsys.chainClusters(subInfo.firstCluster)
	if err != nil {
		t.Fatalf("chainClusters: %v", err)
	}
	if len(clusters) < 2 {
		t.Fatalf("sub only grew to %d clusters, want at least 2", len(clusters))
	}
	wantLen := mustNarrow[uint64](len(clusters), "cluster count") * mustNarrow[uint64](bpc, "bytes per cluster")
	if subInfo.dataLength != wantLen {
		t.Fatalf("sub's stored size = %d, want %d for %d clusters",
			subInfo.dataLength, wantLen, len(clusters))
	}
}

func TestExFatRenameAcrossDirectories(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
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
	content := []byte("cross directory rename, exfat edition")
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

// TestExFatFillVolumeThenNoSpace fills a small volume with one-cluster
// files until allocation refuses, and confirms the refusal is the clean
// ErrNoSpaceOnVolume sentinel, not a partial write or a corrupted volume.
func TestExFatFillVolumeThenNoSpace(t *testing.T) {
	fsys, _ := newExFatTestFS(t, 1<<20) // 1 MiB: small enough to fill in well under a second
	created := 0
	var lastErr error
	for i := range 100000 {
		name := fmt.Sprintf("f%05d.bin", i)
		_, err := fsys.WriteFileStaged(mustPath(t, name), bytes.NewReader([]byte{byte(i)}), true, 0)
		if err != nil {
			lastErr = err
			break
		}
		created++
	}
	if lastErr == nil {
		t.Fatalf("filled the volume with %d files without ever running out of space", created)
	}
	if !errors.Is(lastErr, ErrNoSpaceOnVolume) {
		t.Fatalf("out-of-space error = %v, want ErrNoSpaceOnVolume", lastErr)
	}
	if created == 0 {
		t.Fatalf("ran out of space on the very first file")
	}
	// The refusal must be clean: everything written before it must still
	// read back correctly, and the volume must still accept an ordinary
	// operation like a listing.
	var buf bytes.Buffer
	if err := fsys.ReadFile(mustPath(t, "f00000.bin"), &buf); err != nil {
		t.Fatalf("ReadFile after hitting out-of-space: %v", err)
	}
	if buf.Len() != 1 {
		t.Fatalf("content survived out-of-space with wrong length %d", buf.Len())
	}
	if _, err := fsys.ReadDir(vfs.RootPath()); err != nil {
		t.Fatalf("ReadDir after hitting out-of-space: %v", err)
	}
}

// exfatReserveContiguousForTest marks n free clusters starting at some
// point as allocated in the bitmap, without touching their FAT entries,
// simulating exactly what a NoFatChain extent looks like on disk: its
// clusters are allocated, but the FAT has nothing to say about them.
func exfatReserveContiguousForTest(t *testing.T, fsys *ExFatFS, n int) uint32 {
	t.Helper()
	need := mustNarrow[uint32](n, "test contiguous run length")
	var start uint32
	run := uint32(0)
	for c := uint32(2); c < fsys.bpb.clusterCount+2; c++ {
		allocated, err := fsys.bitmapBit(c)
		if err != nil {
			t.Fatalf("bitmapBit: %v", err)
		}
		if allocated {
			run = 0
			continue
		}
		if run == 0 {
			start = c
		}
		run++
		if run == need {
			break
		}
	}
	if run < need {
		t.Fatalf("could not find %d contiguous free clusters", n)
	}
	for c := start; c < start+need; c++ {
		if err := fsys.setBitmapBit(c, true); err != nil {
			t.Fatalf("setBitmapBit: %v", err)
		}
	}
	fsys.freeCount -= need
	return start
}

// TestExFatReadContiguousNoFatChainFile constructs, by hand, a directory
// entry set whose Stream Extension carries the NoFatChain flag over a
// multi-cluster run, without ever writing a FAT chain for it: exactly the
// on-disk shape this driver's own writer never produces but a real
// formatter or a defragmenter can, and which a reader that always walks
// the FAT would read as garbage or a short read.
func TestExFatReadContiguousNoFatChainFile(t *testing.T) {
	fsys, _ := newExFatTestFS(t, testExFatVolumeSize)
	bpc := int(fsys.bpb.bytesPerCluster())
	content := bytes.Repeat([]byte("no-fat-chain-contiguous-data-"), bpc/10)
	need := (len(content) + bpc - 1) / bpc
	if need < 2 {
		need = 2
	}
	start := exfatReserveContiguousForTest(t, fsys, need)
	if err := fsys.writeAt(content, fsys.clusterOffset(start)); err != nil {
		t.Fatalf("write contiguous content: %v", err)
	}

	set, err := fsys.buildFileEntrySet("contiguous.bin", exAttrArchive, start,
		uint64(len(content)), uint64(len(content)), fsys.clk.Nanos(), true)
	if err != nil {
		t.Fatalf("buildFileEntrySet: %v", err)
	}
	root := fsys.bpb.rootCluster
	if _, aerr := fsys.appendEntries(&root, set); aerr != nil {
		t.Fatalf("appendEntries: %v", aerr)
	}

	// Confirm the FAT genuinely has nothing usable at this range: a
	// reader that ignored NoFatChain and walked the FAT anyway would find
	// cluster 0 (free) rather than a chain, and fail outright.
	first, err := fsys.getFATEntry(start)
	if err != nil {
		t.Fatalf("getFATEntry: %v", err)
	}
	if first != 0 {
		t.Fatalf("test setup leaked a FAT entry for the contiguous run: %#x", first)
	}

	entries, err := fsys.ReadDir(vfs.RootPath())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "contiguous.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ReadDir did not list the NoFatChain file, got %+v", entries)
	}

	var buf bytes.Buffer
	if err := fsys.ReadFile(mustPath(t, "contiguous.bin"), &buf); err != nil {
		t.Fatalf("ReadFile a NoFatChain file: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("NoFatChain content mismatch: got %d bytes, want %d", buf.Len(), len(content))
	}
}

// TestExFatRealFormatterImage formats a real exFAT volume with exfatprogs'
// mkfs.exfat, mounts it with this driver, and confirms the volume label
// and cluster count this driver reads back agree with what mkfs.exfat
// itself wrote. It then writes a file into that same volume through this
// driver and, when fsck.exfat is available, runs it against the result:
// an independent formatter and an independent checker are the only things
// that prove this driver against something other than its own writer.
func TestExFatRealFormatterImage(t *testing.T) {
	mkfsPath, err := exec.LookPath("mkfs.exfat")
	if err != nil {
		t.Skip("mkfs.exfat not available")
	}

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "real.exfat.img")
	const sizeMiB = 64
	f, cerr := os.Create(imgPath)
	if cerr != nil {
		t.Fatalf("create image file: %v", cerr)
	}
	if terr := f.Truncate(sizeMiB << 20); terr != nil {
		t.Fatalf("truncate image file: %v", terr)
	}
	if clerr := f.Close(); clerr != nil {
		t.Fatalf("close image file: %v", clerr)
	}

	if out, merr := mkfsExFat(mkfsPath, imgPath); merr != nil {
		t.Fatalf("mkfs.exfat: %v\n%s", merr, out)
	}

	raw, rerr := os.ReadFile(imgPath)
	if rerr != nil {
		t.Fatalf("read formatted image: %v", rerr)
	}
	dev := newMemDevice(int64(len(raw)))
	if _, werr := dev.WriteAt(raw, 0); werr != nil {
		t.Fatalf("load image into device: %v", werr)
	}

	fsys, merr := MountExFat(dev, mustNarrow[uint64](len(raw), "real exFAT image size"),
		clock.Fixed(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	if merr != nil {
		t.Fatalf("MountExFat a real mkfs.exfat image: %v", merr)
	}

	label, lerr := fsys.Label()
	if lerr != nil {
		t.Fatalf("Label: %v", lerr)
	}
	if label != "TESTVOL" {
		t.Fatalf("Label() = %q, want %q", label, "TESTVOL")
	}
	if fsys.bpb.clusterCount == 0 {
		t.Fatalf("Mount reported zero data clusters for a real image")
	}
	total, _ := fsys.Space()
	wantTotal := uint64(fsys.bpb.clusterCount) * uint64(fsys.bpb.bytesPerCluster())
	if total != wantTotal {
		t.Fatalf("Space total = %d, want %d from the real image's own cluster count", total, wantTotal)
	}

	content := []byte("real exFAT formatter round trip, written by this driver\n")
	p := mustPath(t, "hello.txt")
	if _, werr := fsys.WriteFileStaged(p, bytes.NewReader(content), true, fsys.clk.Nanos()); werr != nil {
		t.Fatalf("WriteFileStaged into a real mkfs.exfat image: %v", werr)
	}
	var buf bytes.Buffer
	if ferr := fsys.ReadFile(p, &buf); ferr != nil {
		t.Fatalf("ReadFile a file this driver wrote into a real image: %v", ferr)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("content mismatch: got %q, want %q", buf.Bytes(), content)
	}

	// Flush this driver's in-memory changes back to the real file so an
	// independent checker sees exactly what this driver wrote.
	if werr := os.WriteFile(imgPath, dev.data, 0o600); werr != nil {
		t.Fatalf("flush device back to image file: %v", werr)
	}

	fsckPath, ferr := exec.LookPath("fsck.exfat")
	if ferr != nil {
		t.Log("fsck.exfat not available, skipping the independent check")
		return
	}
	out, cerr := fsckExFat(fsckPath, imgPath)
	t.Logf("fsck.exfat output:\n%s", out)
	if cerr != nil {
		t.Fatalf("fsck.exfat reported problems on a volume this driver wrote to: %v\n%s", cerr, out)
	}
}

// mkfsExFat and fsckExFat launch the host's own exFAT tools. The tool path
// is a function parameter, so it names what exec.LookPath resolved at the
// point exec.Command reads it, and the image path is under t.TempDir.
func mkfsExFat(tool, imgPath string) ([]byte, error) {
	return exec.Command(tool, "-n", "TESTVOL", imgPath).CombinedOutput()
}

func fsckExFat(tool, imgPath string) ([]byte, error) {
	return exec.Command(tool, "-n", imgPath).CombinedOutput()
}

// TestDefaultUpcaseTableChecksum pins the embedded up-case table against the
// checksum a real formatter writes for it. Every other implementation
// verifies that value when it mounts a volume this driver formatted, so a
// re-encoded or truncated table would make our volumes unreadable everywhere
// else while this driver went on reading them happily.
func TestDefaultUpcaseTableChecksum(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(defaultExFatUpcaseTableB64)
	if err != nil {
		t.Fatalf("decode embedded up-case table: %v", err)
	}
	if got := tableChecksum(raw); got != defaultExFatUpcaseChecksum {
		t.Fatalf("embedded up-case table checksum = %#08x, want %#08x", got, defaultExFatUpcaseChecksum)
	}
}
