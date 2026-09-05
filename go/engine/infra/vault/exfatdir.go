package vault

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// exRawEntry is one 32-byte exFAT directory record. A File entry, a Stream
// Extension entry, a File Name entry, the Allocation Bitmap entry, the
// Up-case Table entry and the Volume Label entry are all this width; only
// the EntryType byte at offset 0 says which, and whether bit 0x80 is set
// says whether the slot is in use.
type exRawEntry [32]byte

const exEntryInUse = 0x80

// exDirentInfo is one resolved directory entry: what a lookup, a listing
// or a rename needs to know, including where its whole entry set (the
// File entry and every secondary after it) sits on disk.
type exDirentInfo struct {
	name            string
	attr            uint16
	firstCluster    uint32
	dataLength      uint64
	validDataLength uint64
	noFatChain      bool
	mtimeNs         int64
	ino             uint64

	entryStart     int64 // offset of the File primary entry
	secondaryCount int   // secondary entries (stream extension + file name runs) following it
}

func (d exDirentInfo) isDir() bool { return d.attr&exAttrDirectory != 0 }

// exDirLocation is a directory this driver is about to read or write:
// its own first cluster, size and NoFatChain state, plus, when it is not
// the volume root, where its own entry set sits so a write that grows its
// chain can patch the stored size back onto disk. exFAT records a
// directory's size explicitly in its Stream Extension entry, unlike FAT,
// which tracks a directory's size only implicitly through its chain
// length; the root carries no such entry, the same way FAT's root has no
// size field either.
type exDirLocation struct {
	cluster    uint32
	dataLength uint64
	noFatChain bool
	isRoot     bool

	ownerCluster   uint32
	entryStart     int64
	secondaryCount int
}

// ensureExtent grows the chain at *start, allocating it fresh if it is
// empty, so it covers at least sizeBytes. It returns every cluster in the
// resulting chain, since the caller is about to address into it by index.
// Only ever called for this driver's own FAT-linked content: nothing this
// driver writes ever produces a NoFatChain extent.
func (fs *ExFatFS) ensureExtent(start *uint32, sizeBytes int64) ([]uint32, error) {
	clusterSize := int64(fs.bpb.bytesPerCluster())
	needed := int((sizeBytes + clusterSize - 1) / clusterSize)
	if needed == 0 {
		needed = 1
	}
	if *start == 0 {
		clusters, err := fs.allocateClusters(needed)
		if err != nil {
			return nil, err
		}
		*start = clusters[0]
		return clusters, nil
	}
	clusters, err := fs.chainClusters(*start)
	if err != nil {
		return nil, err
	}
	if len(clusters) >= needed {
		return clusters, nil
	}
	added, err := fs.extendChain(clusters[len(clusters)-1], needed-len(clusters))
	if err != nil {
		return nil, err
	}
	return append(clusters, added...), nil
}

// writeExtent writes data at byte offset off into the chain at *start,
// growing it as needed.
func (fs *ExFatFS) writeExtent(start *uint32, off int64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	clusters, err := fs.ensureExtent(start, off+int64(len(data)))
	if err != nil {
		return err
	}
	clusterSize := int64(fs.bpb.bytesPerCluster())
	pos := off
	remaining := data
	for len(remaining) > 0 {
		idx := int(pos / clusterSize)
		within := pos % clusterSize
		n := clusterSize - within
		if int64(len(remaining)) < n {
			n = int64(len(remaining))
		}
		if err := fs.writeAt(remaining[:n], fs.clusterOffset(clusters[idx])+within); err != nil {
			return err
		}
		remaining = remaining[n:]
		pos += n
	}
	return nil
}

// readExtent reads len(buf) bytes at byte offset off from the content at
// start, honoring noFatChain the way clusterList documents. dataLength is
// the content's own allocated size, consulted only to size a contiguous
// extent's implicit cluster run; a FAT-linked read ignores it.
func (fs *ExFatFS) readExtent(start uint32, noFatChain bool, dataLength uint64, off int64, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	clusters, err := fs.clusterList(start, dataLength, noFatChain)
	if err != nil {
		return err
	}
	clusterSize := int64(fs.bpb.bytesPerCluster())
	pos := off
	remaining := buf
	for len(remaining) > 0 {
		idx := int(pos / clusterSize)
		if idx >= len(clusters) {
			return io.ErrUnexpectedEOF
		}
		within := pos % clusterSize
		n := clusterSize - within
		if int64(len(remaining)) < n {
			n = int64(len(remaining))
		}
		if err := fs.readAt(remaining[:n], fs.clusterOffset(clusters[idx])+within); err != nil {
			return err
		}
		remaining = remaining[n:]
		pos += n
	}
	return nil
}

// scanRawEntries walks every 32-byte slot of the directory at start,
// including unused, deleted and secondary slots, calling fn with each and
// its byte offset. It stops when fn returns true or the allocated area is
// exhausted.
func (fs *ExFatFS) scanRawEntries(start uint32, noFatChain bool, dataLength uint64, fn func(e exRawEntry, off int64) bool) error {
	clusters, err := fs.clusterList(start, dataLength, noFatChain)
	if err != nil {
		return err
	}
	total := int64(len(clusters)) * int64(fs.bpb.bytesPerCluster())
	for off := int64(0); off < total; off += 32 {
		var e exRawEntry
		if err := fs.readExtent(start, noFatChain, dataLength, off, e[:]); err != nil {
			return err
		}
		if fn(e, off) {
			return nil
		}
	}
	return nil
}

// scanExDir walks the logical entries of the directory at start: one call
// to fn per File entry, its whole entry set already validated and
// decoded. A slot whose EntryType is not a File entry is a system entry
// (the allocation bitmap, the up-case table, the volume label, a volume
// GUID this driver never writes) or a deleted slot; either way it is not
// a name the vfs layer sees, and is skipped rather than surfaced. exFAT
// keeps no "." or ".." entries the way FAT does: a directory's parent is
// never looked up from inside it, only walked down to from the root.
func (fs *ExFatFS) scanExDir(start uint32, noFatChain bool, dataLength uint64, fn func(exDirentInfo) bool) error {
	clusters, err := fs.clusterList(start, dataLength, noFatChain)
	if err != nil {
		return err
	}
	total := int64(len(clusters)) * int64(fs.bpb.bytesPerCluster())
	off := int64(0)
	for off < total {
		var file exRawEntry
		if err := fs.readExtent(start, noFatChain, dataLength, off, file[:]); err != nil {
			return err
		}
		switch {
		case file[0] == 0x00:
			return nil // end of directory
		case file[0]&exEntryInUse == 0:
			off += 32 // deleted or never-used slot
			continue
		case file[0] != exEntryFile:
			off += 32 // a system entry occupying exactly one slot
			continue
		}
		secondaryCount := int(file[1])
		if secondaryCount < 2 || int64(secondaryCount+1)*32 > total-off {
			return fmt.Errorf("%w: file entry at offset %d claims %d secondary entries past the directory's end",
				ErrCorruptDirectory, off, secondaryCount)
		}
		set := make([]exRawEntry, secondaryCount+1)
		set[0] = file
		for i := 1; i <= secondaryCount; i++ {
			if err := fs.readExtent(start, noFatChain, dataLength, off+int64(i)*32, set[i][:]); err != nil {
				return err
			}
		}
		info, err := fs.decodeEntrySet(set, off)
		if err != nil {
			return err
		}
		info.ino = entryIno(info.firstCluster, start, off)
		if fn(info) {
			return nil
		}
		off += int64(secondaryCount+1) * 32
	}
	return nil
}

// findEntry looks up name among dir's logical entries, case-sensitively
// against the decoded name: this driver always writes an entry set with
// the exact name it was given, so an exact match is what every entry this
// driver created deserves, the same simplification fatdir.go's own
// findEntry documents.
func (fs *ExFatFS) findEntry(dir uint32, noFatChain bool, dataLength uint64, name string) (exDirentInfo, bool, error) {
	var found exDirentInfo
	ok := false
	err := fs.scanExDir(dir, noFatChain, dataLength, func(d exDirentInfo) bool {
		if d.name == name {
			found = d
			ok = true
			return true
		}
		return false
	})
	return found, ok, err
}

// resolve walks p component by component from the root directory. The
// root path itself resolves to a synthetic entry describing the root
// directory, the same convention fatdir.go's resolve uses.
func (fs *ExFatFS) resolve(p vfs.SafePath) (exDirentInfo, error) {
	comps := p.Components()
	if len(comps) == 0 {
		return exDirentInfo{
			name:         "",
			attr:         exAttrDirectory,
			firstCluster: fs.bpb.rootCluster,
			ino:          deriveIno(0, 0),
		}, nil
	}
	dir := fs.bpb.rootCluster
	noFatChain := false
	var dataLength uint64
	var info exDirentInfo
	for i, name := range comps {
		if err := checkExFatName(name); err != nil {
			return exDirentInfo{}, err
		}
		found, ok, err := fs.findEntry(dir, noFatChain, dataLength, name)
		if err != nil {
			return exDirentInfo{}, err
		}
		if !ok {
			return exDirentInfo{}, fmt.Errorf("resolve %q: %w", p.String(), vfs.ErrNotFound)
		}
		if i < len(comps)-1 {
			if !found.isDir() {
				return exDirentInfo{}, fmt.Errorf("resolve %q: %w", p.String(), vfs.ErrNotFound)
			}
			dir = found.firstCluster
			noFatChain = found.noFatChain
			dataLength = found.dataLength
		}
		info = found
	}
	return info, nil
}

// resolveParent resolves p's parent directory and validates p's own leaf
// name, without requiring the leaf to already exist. When the parent is
// not the volume root, it also locates the parent's own entry in the
// grandparent directory, so a caller that grows the parent's chain can
// patch the parent's stored size back onto disk.
func (fs *ExFatFS) resolveParent(p vfs.SafePath) (exDirLocation, string, error) {
	if p.IsRoot() {
		return exDirLocation{}, "", fmt.Errorf("%w: the share root has no parent", vfs.ErrDenied)
	}
	leaf := p.Name()
	if err := checkExFatName(leaf); err != nil {
		return exDirLocation{}, "", err
	}
	parentPath := p.Parent()
	parentInfo, err := fs.resolve(parentPath)
	if err != nil {
		return exDirLocation{}, "", err
	}
	if !parentInfo.isDir() && !parentPath.IsRoot() {
		return exDirLocation{}, "", fmt.Errorf("resolve %q: %w", p.String(), vfs.ErrNotFound)
	}
	loc := exDirLocation{
		cluster:    parentInfo.firstCluster,
		dataLength: parentInfo.dataLength,
		noFatChain: parentInfo.noFatChain,
		isRoot:     parentPath.IsRoot(),
	}
	if loc.isRoot {
		return loc, leaf, nil
	}
	gpPath := parentPath.Parent()
	gpInfo, err := fs.resolve(gpPath)
	if err != nil {
		return exDirLocation{}, "", err
	}
	gpCluster := gpInfo.firstCluster
	var gpNoFatChain bool
	var gpDataLength uint64
	if !gpPath.IsRoot() {
		gpNoFatChain = gpInfo.noFatChain
		gpDataLength = gpInfo.dataLength
	}
	ownEntry, ok, err := fs.findEntry(gpCluster, gpNoFatChain, gpDataLength, parentPath.Name())
	if err != nil {
		return exDirLocation{}, "", err
	}
	if !ok {
		return exDirLocation{}, "", fmt.Errorf("resolve %q: parent vanished: %w", p.String(), vfs.ErrNotFound)
	}
	loc.ownerCluster = gpCluster
	loc.entryStart = ownEntry.entryStart
	loc.secondaryCount = ownEntry.secondaryCount
	return loc, leaf, nil
}

// findInsertPoint is the byte offset of the first unused (0x00) slot in
// dir, or the byte just past the end of its current chain if every slot
// is in use. Only ever called for this driver's own FAT-linked
// directories, so noFatChain and dataLength are always false and zero.
func (fs *ExFatFS) findInsertPoint(dir uint32) (int64, error) {
	pos := int64(-1)
	err := fs.scanRawEntries(dir, false, 0, func(e exRawEntry, off int64) bool {
		if e[0] == 0x00 {
			pos = off
			return true
		}
		return false
	})
	if err != nil {
		return 0, err
	}
	if pos >= 0 {
		return pos, nil
	}
	clusters, err := fs.chainClusters(dir)
	if err != nil {
		return 0, err
	}
	return int64(len(clusters)) * int64(fs.bpb.bytesPerCluster()), nil
}

// appendEntries writes entries into dir starting at the current insertion
// point and re-terminates the directory with a fresh end marker.
func (fs *ExFatFS) appendEntries(dir *uint32, entries []exRawEntry) (int64, error) {
	insertAt, err := fs.findInsertPoint(*dir)
	if err != nil {
		return 0, err
	}
	for i, e := range entries {
		if err := fs.writeExtent(dir, insertAt+int64(i)*32, e[:]); err != nil {
			return 0, err
		}
	}
	var marker exRawEntry
	if err := fs.writeExtent(dir, insertAt+int64(len(entries))*32, marker[:]); err != nil {
		return 0, err
	}
	return insertAt, nil
}

// appendToDir appends entries to loc's directory and, unless loc is the
// volume root, patches loc's own stored size in its parent so a growth
// this append caused is not lost the next time anything reads loc's
// entry.
func (fs *ExFatFS) appendToDir(loc *exDirLocation, entries []exRawEntry) (int64, error) {
	insertAt, err := fs.appendEntries(&loc.cluster, entries)
	if err != nil {
		return 0, err
	}
	if !loc.isRoot {
		if err := fs.syncDirSize(loc); err != nil {
			return 0, err
		}
	}
	return insertAt, nil
}

// syncDirSize recomputes loc's cluster chain length and patches it into
// loc's own Stream Extension entry, in loc's parent directory.
func (fs *ExFatFS) syncDirSize(loc *exDirLocation) error {
	clusters, err := fs.chainClusters(loc.cluster)
	if err != nil {
		return err
	}
	size := uint64(len(clusters)) * uint64(fs.bpb.bytesPerCluster())
	loc.dataLength = size
	return fs.updateEntrySet(loc.ownerCluster, loc.entryStart, loc.secondaryCount, func(set []exRawEntry) {
		binary.LittleEndian.PutUint64(set[1][8:16], size)
		binary.LittleEndian.PutUint64(set[1][24:32], size)
	})
}

// updateEntrySet reads an entry set of secondaryCount+1 slots from dir at
// entryStart, lets patch mutate it, recomputes its checksum and writes it
// back. Only ever called against this driver's own FAT-linked
// directories.
func (fs *ExFatFS) updateEntrySet(dir uint32, entryStart int64, secondaryCount int, patch func(set []exRawEntry)) error {
	set := make([]exRawEntry, secondaryCount+1)
	for i := range set {
		if err := fs.readExtent(dir, false, 0, entryStart+int64(i)*32, set[i][:]); err != nil {
			return err
		}
	}
	patch(set)
	checksum := exEntrySetChecksum(set)
	binary.LittleEndian.PutUint16(set[0][2:4], checksum)
	d := dir
	for i, e := range set {
		if err := fs.writeExtent(&d, entryStart+int64(i)*32, e[:]); err != nil {
			return err
		}
	}
	return nil
}

// deleteEntries clears the in-use bit on every slot in [entryStart,
// entryEnd), freeing the whole entry set at once. Only the type bit is
// touched, the same discipline fatdir.go's deleteEntries uses, so a scan
// mid-set still knows how many slots to skip.
func (fs *ExFatFS) deleteEntries(dir uint32, entryStart, entryEnd int64) error {
	for off := entryStart; off < entryEnd; off += 32 {
		var b [1]byte
		if err := fs.readExtent(dir, false, 0, off, b[:]); err != nil {
			return err
		}
		b[0] &^= exEntryInUse
		if err := fs.writeExtent(&dir, off, b[:]); err != nil {
			return err
		}
	}
	return nil
}

// createEntry mints an entry set for name and appends it to loc.
func (fs *ExFatFS) createEntry(loc *exDirLocation, name string, attr uint16, cluster uint32, dataLength, validDataLength uint64, mtimeNs int64, noFatChain bool) error {
	set, err := fs.buildFileEntrySet(name, attr, cluster, dataLength, validDataLength, mtimeNs, noFatChain)
	if err != nil {
		return err
	}
	_, err = fs.appendToDir(loc, set)
	return err
}

// Stat resolves p and reports its kind, size and modification time.
func (fs *ExFatFS) Stat(p vfs.SafePath) (StatInfo, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	info, err := fs.resolve(p)
	if err != nil {
		return StatInfo{}, err
	}
	return StatInfo{
		IsDir:   info.isDir(),
		Size:    info.dataLength,
		MtimeNs: info.mtimeNs,
		Ino:     info.ino,
	}, nil
}

// ReadDir lists dir's entries. exFAT stores no "." or ".." entries and no
// volume-label file, so nothing needs excluding the way fatdir.go's
// ReadDir excludes them.
func (fs *ExFatFS) ReadDir(p vfs.SafePath) ([]Dirent, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	info, err := fs.resolve(p)
	if err != nil {
		return nil, err
	}
	if !info.isDir() {
		return nil, fmt.Errorf("read dir %q: %w", p.String(), vfs.ErrNotFound)
	}
	dir := fs.bpb.rootCluster
	noFatChain := false
	var dataLength uint64
	if !p.IsRoot() {
		dir = info.firstCluster
		noFatChain = info.noFatChain
		dataLength = info.dataLength
	}
	var entries []Dirent
	err = fs.scanExDir(dir, noFatChain, dataLength, func(d exDirentInfo) bool {
		entries = append(entries, Dirent{Name: d.name, IsDir: d.isDir(), Ino: d.ino})
		return false
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// Mkdir creates an empty directory at p, refusing if anything already
// occupies the name. The new directory's own cluster needs only a single
// end-of-directory marker: exFAT keeps no "." or ".." entries to seed.
func (fs *ExFatFS) Mkdir(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	loc, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	if _, ok, ferr := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, leaf); ferr != nil {
		return ferr
	} else if ok {
		return fmt.Errorf("mkdir %q: %w", p.String(), vfs.ErrExists)
	}
	clusters, err := fs.allocateClusters(1)
	if err != nil {
		return err
	}
	newCluster := clusters[0]
	var marker exRawEntry
	if err := fs.writeCluster(newCluster, marker[:]); err != nil {
		return err
	}
	now := fs.clk.Nanos()
	bpc := uint64(fs.bpb.bytesPerCluster())
	return fs.createEntry(&loc, leaf, exAttrDirectory, newCluster, bpc, bpc, now, false)
}

// Rmdir removes an empty directory. A directory holding anything is
// refused, never emptied on the caller's behalf.
func (fs *ExFatFS) Rmdir(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if p.IsRoot() {
		return fmt.Errorf("rmdir: %w", vfs.ErrDenied)
	}
	loc, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, leaf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("rmdir %q: %w", p.String(), vfs.ErrNotFound)
	}
	if !entry.isDir() {
		return fmt.Errorf("rmdir %q: %w", p.String(), vfs.ErrNotADirectory)
	}
	empty := true
	err = fs.scanExDir(entry.firstCluster, entry.noFatChain, entry.dataLength, func(d exDirentInfo) bool {
		empty = false
		return true
	})
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("rmdir %q: %w", p.String(), vfs.ErrNotEmpty)
	}
	if err := fs.freeContent(entry); err != nil {
		return err
	}
	return fs.deleteEntries(loc.cluster, entry.entryStart, entry.entryStart+int64(entry.secondaryCount+1)*32)
}

// freeContent releases entry's clusters, choosing bitmap-only release for
// a NoFatChain extent (its FAT entries were never this driver's to
// maintain) or FAT-chain release otherwise.
func (fs *ExFatFS) freeContent(entry exDirentInfo) error {
	if entry.firstCluster == 0 {
		return nil
	}
	if entry.noFatChain {
		return fs.freeContiguous(entry.firstCluster, entry.dataLength)
	}
	return fs.freeChain(entry.firstCluster)
}

// CreateFile creates an empty file at p, refusing if anything already
// occupies the name.
func (fs *ExFatFS) CreateFile(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	loc, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	if _, ok, err := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, leaf); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("create %q: %w", p.String(), vfs.ErrExists)
	}
	return fs.createEntry(&loc, leaf, exAttrArchive, 0, 0, 0, fs.clk.Nanos(), false)
}

// Remove deletes a file. A directory at p is refused with ErrIsDirectory,
// the mirror of Rmdir's refusal of a plain file.
func (fs *ExFatFS) Remove(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	loc, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, leaf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("remove %q: %w", p.String(), vfs.ErrNotFound)
	}
	if entry.isDir() {
		return fmt.Errorf("remove %q: %w", p.String(), vfs.ErrIsDirectory)
	}
	if err := fs.freeContent(entry); err != nil {
		return err
	}
	return fs.deleteEntries(loc.cluster, entry.entryStart, entry.entryStart+int64(entry.secondaryCount+1)*32)
}

// ReadFile streams a file's whole content to w.
func (fs *ExFatFS) ReadFile(p vfs.SafePath, w io.Writer) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	info, err := fs.resolve(p)
	if err != nil {
		return err
	}
	if info.isDir() {
		return fmt.Errorf("read %q: %w", p.String(), vfs.ErrIsDirectory)
	}
	return fs.streamFile(info.firstCluster, info.dataLength, info.validDataLength, info.noFatChain, w)
}

// streamFile writes dataLength bytes to w: whatever of it lies below
// validDataLength comes from the device, and the rest, if any, is
// synthesized as zero, exactly as the exFAT specification requires for
// content a writer allocated but never actually wrote.
func (fs *ExFatFS) streamFile(start uint32, dataLength, validDataLength uint64, noFatChain bool, w io.Writer) error {
	const chunk = 256 << 10
	buf := make([]byte, chunk)
	for off := uint64(0); off < dataLength; off += chunk {
		n := dataLength - off
		if n > chunk {
			n = chunk
		}
		clear(buf[:n])
		if off < validDataLength {
			readN := n
			if off+readN > validDataLength {
				readN = validDataLength - off
			}
			if err := fs.readExtent(start, noFatChain, dataLength, mustNarrow[int64](off, "file read offset"), buf[:readN]); err != nil {
				return err
			}
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return err
		}
	}
	return nil
}

// stagingSuffix mints a random name suffix for a durable write's staging
// entry, so two concurrent writes aimed at the same destination never
// collide on the same staging name.
func exfatStagingSuffix() []byte {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("vault: system randomness unavailable: " + err.Error())
	}
	return b
}

// WriteFileStaged writes r's entire content into the exFAT filesystem
// under a staging name in dest's own parent directory, then renames it
// onto dest, the same atomic-from-a-reader's-view construction
// fatdir.go's WriteFileStaged documents.
func (fs *ExFatFS) WriteFileStaged(dest vfs.SafePath, r io.Reader, noClobber bool, mtimeNs int64) (bool, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	loc, leaf, err := fs.resolveParent(dest)
	if err != nil {
		return false, err
	}
	existingEntry, exists, err := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, leaf)
	if err != nil {
		return false, err
	}
	if exists && noClobber {
		return false, fmt.Errorf("write %q: %w", dest.String(), vfs.ErrExists)
	}
	if exists && existingEntry.isDir() {
		return false, fmt.Errorf("write %q: %w", dest.String(), vfs.ErrIsDirectory)
	}

	stagingName := fmt.Sprintf(".stowstage-%x", exfatStagingSuffix())
	if cerr := fs.createEntry(&loc, stagingName, exAttrArchive, 0, 0, 0, mtimeNs, false); cerr != nil {
		return false, cerr
	}
	stagingEntry, ok, err := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, stagingName)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("write %q: staging entry vanished: %w", dest.String(), vfs.ErrNotFound)
	}

	var written int64
	var cluster uint32
	buf := make([]byte, 256<<10)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if err := fs.writeExtent(&cluster, written, buf[:n]); err != nil {
				return false, err
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return false, rerr
		}
	}
	size := mustNarrow[uint64](written, "staged file size")
	if err := fs.updateEntrySet(loc.cluster, stagingEntry.entryStart, stagingEntry.secondaryCount, func(set []exRawEntry) {
		binary.LittleEndian.PutUint32(set[1][20:24], cluster)
		binary.LittleEndian.PutUint64(set[1][8:16], size)
		binary.LittleEndian.PutUint64(set[1][24:32], size)
		if cluster != 0 {
			set[1][1] |= exFlagAllocationPossible
		}
		date, tm := nsToFATTime(mtimeNs)
		ts := uint32(tm) | uint32(date)<<16
		binary.LittleEndian.PutUint32(set[0][12:16], ts)
	}); err != nil {
		return false, err
	}

	if exists {
		if err := fs.freeContent(existingEntry); err != nil {
			return false, err
		}
		if err := fs.deleteEntries(loc.cluster, existingEntry.entryStart, existingEntry.entryStart+int64(existingEntry.secondaryCount+1)*32); err != nil {
			return false, err
		}
	}
	if err := fs.renameEntry(loc.cluster, stagingName, &loc, leaf); err != nil {
		return false, err
	}
	return exists, nil
}

// SetModTime patches p's last-modified timestamp.
func (fs *ExFatFS) SetModTime(p vfs.SafePath, mtimeNs int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if p.IsRoot() {
		return fmt.Errorf("set mod time: %w", vfs.ErrDenied)
	}
	loc, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, leaf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("set mod time %q: %w", p.String(), vfs.ErrNotFound)
	}
	return fs.updateEntrySet(loc.cluster, entry.entryStart, entry.secondaryCount, func(set []exRawEntry) {
		date, tm := nsToFATTime(mtimeNs)
		ts := uint32(tm) | uint32(date)<<16
		binary.LittleEndian.PutUint32(set[0][12:16], ts)
	})
}

// materializeChain converts a NoFatChain extent's implicit cluster run
// into an ordinary FAT chain, so it can be grown or shrunk like any other:
// its FAT entries, never maintained while it was contiguous, are written
// for the first time here.
func (fs *ExFatFS) materializeChain(start uint32, dataLength uint64) error {
	clusters, err := fs.clusterList(start, dataLength, true)
	if err != nil {
		return err
	}
	for i, c := range clusters {
		value := uint32(exfatEOC)
		if i+1 < len(clusters) {
			value = clusters[i+1]
		}
		if err := fs.setFATEntry(c, value); err != nil {
			return err
		}
	}
	return nil
}

// Truncate resizes a file, zero-filling any newly exposed bytes when
// growing and freeing whatever it no longer needs when shrinking.
func (fs *ExFatFS) Truncate(p vfs.SafePath, newSize uint64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	loc, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(loc.cluster, loc.noFatChain, loc.dataLength, leaf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("truncate %q: %w", p.String(), vfs.ErrNotFound)
	}
	if entry.isDir() {
		return fmt.Errorf("truncate %q: %w", p.String(), vfs.ErrIsDirectory)
	}

	cluster := entry.firstCluster
	// A contiguous extent has no FAT chain to walk, so it is converted to
	// an ordinary one before anything below grows or shrinks it.
	if entry.noFatChain && cluster != 0 {
		if err := fs.materializeChain(cluster, entry.dataLength); err != nil {
			return err
		}
	}
	clusterSize := uint64(fs.bpb.bytesPerCluster())
	oldSize := entry.dataLength
	switch {
	case newSize == 0:
		if cluster != 0 {
			if err := fs.freeChain(cluster); err != nil {
				return err
			}
		}
		cluster = 0
	case newSize < oldSize:
		keep := mustNarrow[int]((newSize+clusterSize-1)/clusterSize, "retained cluster count")
		if keep == 0 {
			keep = 1
		}
		if cluster != 0 {
			if err := fs.truncateChainAfter(cluster, keep); err != nil {
				return err
			}
		}
	case newSize > oldSize:
		zeros := make([]byte, 256<<10)
		for off := oldSize; off < newSize; {
			n := newSize - off
			if n > uint64(len(zeros)) {
				n = uint64(len(zeros))
			}
			if err := fs.writeExtent(&cluster, mustNarrow[int64](off, "truncate write offset"), zeros[:n]); err != nil {
				return err
			}
			off += n
		}
	}
	return fs.updateEntrySet(loc.cluster, entry.entryStart, entry.secondaryCount, func(set []exRawEntry) {
		binary.LittleEndian.PutUint32(set[1][20:24], cluster)
		binary.LittleEndian.PutUint64(set[1][8:16], newSize)
		binary.LittleEndian.PutUint64(set[1][24:32], newSize)
		if cluster != 0 {
			set[1][1] |= exFlagAllocationPossible
		} else {
			set[1][1] &^= exFlagAllocationPossible
		}
		set[1][1] &^= exFlagNoFatChain
	})
}

// renameEntry moves the logical entry named fromName in fromDir to toName
// in toLoc, by appending a fresh entry set naming the same content to the
// destination and deleting the original slots. The content clusters are
// untouched; only the directory metadata naming them moves.
func (fs *ExFatFS) renameEntry(fromDir uint32, fromName string, toLoc *exDirLocation, toName string) error {
	entry, ok, err := fs.findEntry(fromDir, false, 0, fromName)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("rename: source vanished: %w", vfs.ErrNotFound)
	}
	set, err := fs.buildFileEntrySet(toName, entry.attr, entry.firstCluster, entry.dataLength, entry.validDataLength, entry.mtimeNs, entry.noFatChain)
	if err != nil {
		return err
	}
	if _, err := fs.appendToDir(toLoc, set); err != nil {
		return err
	}
	return fs.deleteEntries(fromDir, entry.entryStart, entry.entryStart+int64(entry.secondaryCount+1)*32)
}

// Rename moves the entry at from to to, within this exFAT filesystem.
// noReplace refuses with vfs.ErrExists when to already names something.
func (fs *ExFatFS) Rename(from, to vfs.SafePath, noReplace bool) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if from.IsRoot() || to.IsRoot() {
		return fmt.Errorf("rename: %w", vfs.ErrDenied)
	}
	fromLoc, fromLeaf, err := fs.resolveParent(from)
	if err != nil {
		return err
	}
	toLoc, toLeaf, err := fs.resolveParent(to)
	if err != nil {
		return err
	}
	if existing, ok, err := fs.findEntry(toLoc.cluster, toLoc.noFatChain, toLoc.dataLength, toLeaf); err != nil {
		return err
	} else if ok {
		if noReplace {
			return fmt.Errorf("rename %q: %w", to.String(), vfs.ErrExists)
		}
		if existing.isDir() {
			return fmt.Errorf("rename %q: %w", to.String(), vfs.ErrIsDirectory)
		}
		if err := fs.freeContent(existing); err != nil {
			return err
		}
		if err := fs.deleteEntries(toLoc.cluster, existing.entryStart, existing.entryStart+int64(existing.secondaryCount+1)*32); err != nil {
			return err
		}
	}
	return fs.renameEntry(fromLoc.cluster, fromLeaf, &toLoc, toLeaf)
}

// Label reads the volume label, or "" if the volume carries none: the
// spec-compliant state a volume with no Volume Label entry in its root
// directory is in.
func (fs *ExFatFS) Label() (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var label string
	err := fs.scanRawEntries(fs.bpb.rootCluster, false, 0, func(e exRawEntry, off int64) bool {
		if e[0] == exEntryLabel {
			label = decodeExFatLabel(e)
			return true
		}
		return false
	})
	return label, err
}
