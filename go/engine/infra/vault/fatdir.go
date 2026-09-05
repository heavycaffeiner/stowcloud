package vault

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// rawDirEntry is one 32-byte FAT directory record, short or long-name.
type rawDirEntry [32]byte

// Directory entry attribute bits, straight from the FAT specification.
const (
	attrReadOnly  = 0x01
	attrHidden    = 0x02
	attrSystem    = 0x04
	attrVolumeID  = 0x08
	attrDirectory = 0x10
	attrArchive   = 0x20
	attrLongName  = attrReadOnly | attrHidden | attrSystem | attrVolumeID
)

const (
	direntFree    = 0x00
	direntDeleted = 0xE5
)

// direntInfo is one resolved directory entry: what a lookup, a listing or a
// rename needs to know, including exactly where on disk its long-name run
// and short entry live so a caller can delete or patch it without scanning
// again.
type direntInfo struct {
	name         string
	attr         byte
	firstCluster uint32
	size         uint32
	mtimeNs      int64
	ino          uint64

	entryStart  int64 // offset of the first entry (an LFN entry, or the short entry if there is none)
	shortOffset int64 // offset of the short entry itself
}

func (d direntInfo) isDir() bool { return d.attr&attrDirectory != 0 }

// inoHighBit marks every inode this driver derives from an entry's
// location rather than from its content cluster. A real FAT32 cluster
// number fits in 28 bits (maxTotalClusters), so no genuine first-cluster
// inode can ever carry this bit: a derived inode and a content-cluster
// inode can never collide.
const inoHighBit = uint64(1) << 63

// deriveIno synthesizes a stable inode from a location: the cluster of the
// directory holding the entry, and the entry's own byte offset within it.
// Directory cluster 0 does not exist for any real directory (every chain
// starts at cluster 2 or later), so it is reserved for the volume root's
// own fixed identity.
func deriveIno(dirCluster uint32, entryOffset int64) uint64 {
	offset := mustNarrow[uint64](entryOffset, "directory entry offset")
	return inoHighBit | (offset / 32 << 28) | uint64(dirCluster&0x0FFFFFFF)
}

// entryIno is the inode a resolved entry reports. An entry with content
// keeps its first cluster: that number is stable across a rename, which is
// what lets a caller track a renamed file by inode, and two chains can
// never share a first cluster at once, so it never collides with another
// entry's identity.
//
// A zero-length file owns no cluster and would otherwise share inode 0
// with every other empty file in the volume, which is exactly the
// collision an identity tuple keyed on (dev, ino) cannot tolerate: two
// unrelated empty files would claim one identity, and a link or a cache
// row pinned to one could resolve to the other. Deriving its inode from
// its directory entry's own location instead is the trade this driver
// makes there: an empty file has no content whose identity has to survive
// a rename, only a name that would leave its old location, so the inode
// is free to change when the entry does.
func entryIno(firstCluster uint32, dirCluster uint32, entryOffset int64) uint64 {
	if firstCluster != 0 {
		return uint64(firstCluster)
	}
	return deriveIno(dirCluster, entryOffset)
}

// ensureExtent grows the chain at *start, allocating it fresh if it is
// empty, so it covers at least sizeBytes. It returns every cluster in the
// resulting chain, since the caller is about to address into it by index.
func (fs *FS) ensureExtent(start *uint32, sizeBytes int64) ([]uint32, error) {
	clusterSize := int64(fs.bytesPerCluster())
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
// growing it as needed. *start moves from 0 to the newly allocated first
// cluster the first time a zero-length chain is written to.
func (fs *FS) writeExtent(start *uint32, off int64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	clusters, err := fs.ensureExtent(start, off+int64(len(data)))
	if err != nil {
		return err
	}
	clusterSize := int64(fs.bytesPerCluster())
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

// readExtent reads len(buf) bytes at byte offset off from the chain at
// start. The caller bounds off and len(buf) to what it already knows the
// entry's logical size to be.
func (fs *FS) readExtent(start uint32, off int64, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	clusters, err := fs.chainClusters(start)
	if err != nil {
		return err
	}
	clusterSize := int64(fs.bytesPerCluster())
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

// dirRegionSize is the byte size of the directory at dir: the FAT12/FAT16
// fixed root's constant size, or a cluster chain's current length times the
// cluster size.
func (fs *FS) dirRegionSize(dir uint32) (int64, error) {
	if dir == fatFixedRoot {
		return fs.fixedRootSize(), nil
	}
	clusters, err := fs.chainClusters(dir)
	if err != nil {
		return 0, err
	}
	return int64(len(clusters)) * int64(fs.bytesPerCluster()), nil
}

// dirReadAt reads len(buf) bytes at byte offset off within the directory at
// dir, taking the fixed root's own addressing when dir is fatFixedRoot.
func (fs *FS) dirReadAt(dir uint32, off int64, buf []byte) error {
	if dir == fatFixedRoot {
		if off+int64(len(buf)) > fs.fixedRootSize() {
			return io.ErrUnexpectedEOF
		}
		return fs.readAt(buf, fs.fixedRootOffset()+off)
	}
	return fs.readExtent(dir, off, buf)
}

// dirWriteAt writes buf at byte offset off within the directory at *dir. A
// write past the fixed root's constant size is refused with
// ErrNoSpaceOnVolume rather than spilling into the data area that starts
// immediately after it; a cluster-chain directory grows instead, exactly as
// writeExtent already does.
func (fs *FS) dirWriteAt(dir *uint32, off int64, buf []byte) error {
	if *dir == fatFixedRoot {
		if off+int64(len(buf)) > fs.fixedRootSize() {
			return ErrNoSpaceOnVolume
		}
		return fs.writeAt(buf, fs.fixedRootOffset()+off)
	}
	return fs.writeExtent(dir, off, buf)
}

// scanRawEntries walks every 32-byte slot of the directory at start,
// including free, deleted and long-name slots, calling fn with each and its
// byte offset. It stops when fn returns true or the allocated area is
// exhausted; it never sees the 0x00 end marker as a reason to stop on its
// own, since callers that need the insertion point ask for exactly that
// slot.
func (fs *FS) scanRawEntries(start uint32, fn func(e rawDirEntry, off int64) bool) error {
	total, err := fs.dirRegionSize(start)
	if err != nil {
		return err
	}
	for off := int64(0); off < total; off += 32 {
		var e rawDirEntry
		if err := fs.dirReadAt(start, off, e[:]); err != nil {
			return err
		}
		if fn(e, off) {
			return nil
		}
	}
	return nil
}

// scanDir walks the logical entries of the directory at start: one call to
// fn per short entry, its long name already reassembled from any VFAT
// entries immediately preceding it. It stops at the first free (0x00) slot,
// which is FAT's own end-of-directory marker.
func (fs *FS) scanDir(start uint32, fn func(direntInfo) bool) error {
	var pendingStart int64 = -1
	lfnParts := map[int]string{}
	var lfnChecksumWant byte
	err := fs.scanRawEntries(start, func(e rawDirEntry, off int64) bool {
		if e[0] == direntFree {
			return true
		}
		if e[0] == direntDeleted {
			pendingStart = -1
			lfnParts = map[int]string{}
			return false
		}
		if e[11] == attrLongName {
			ord := int(e[0] &^ 0x40)
			if e[0]&0x40 != 0 {
				lfnParts = map[int]string{}
				lfnChecksumWant = e[13]
				pendingStart = off
			}
			lfnParts[ord] = string(utf16.Decode(decodeLFNChunk(e)))
			return false
		}
		var shortKey [11]byte
		copy(shortKey[:], e[0:11])
		entryStart := off
		name := shortNameToString(shortKey, e[12])
		if pendingStart >= 0 && lfnChecksum(shortKey) == lfnChecksumWant && len(lfnParts) > 0 {
			var sb strings.Builder
			for i := 1; i <= len(lfnParts); i++ {
				if _, err := sb.WriteString(lfnParts[i]); err != nil {
					panicInfallibleWrite(err)
				}
			}
			name = sb.String()
			entryStart = pendingStart
		}
		pendingStart = -1
		lfnParts = map[int]string{}
		if e[11]&attrVolumeID != 0 {
			return false
		}
		firstCluster := uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 | uint32(binary.LittleEndian.Uint16(e[26:28]))
		info := direntInfo{
			name:         name,
			attr:         e[11],
			firstCluster: firstCluster,
			size:         binary.LittleEndian.Uint32(e[28:32]),
			mtimeNs:      fatTimeToNs(binary.LittleEndian.Uint16(e[24:26]), binary.LittleEndian.Uint16(e[22:24])),
			ino:          entryIno(firstCluster, start, off),
			entryStart:   entryStart,
			shortOffset:  off,
		}
		return fn(info)
	})
	return err
}

// dirShortNames answers generateShortName's uniqueness question by scanning
// a directory's raw short-name slots once.
type dirShortNames struct {
	names map[[11]byte]bool
}

func (s *dirShortNames) hasShortName(p [11]byte) bool { return s.names[p] }

func (fs *FS) collectShortNames(dirStart uint32) (*dirShortNames, error) {
	set := &dirShortNames{names: map[[11]byte]bool{}}
	err := fs.scanRawEntries(dirStart, func(e rawDirEntry, off int64) bool {
		if e[0] == direntFree {
			return true
		}
		if e[0] == direntDeleted || e[11] == attrLongName {
			return false
		}
		var key [11]byte
		copy(key[:], e[0:11])
		set.names[key] = true
		return false
	})
	return set, err
}

// findEntry looks up name (already validated by checkFATName) among dir's
// logical entries, case-sensitively against the reassembled long name: this
// driver always writes a long-name run with the exact name it was given, so
// an exact match is what every entry this driver created deserves.
func (fs *FS) findEntry(dir uint32, name string) (direntInfo, bool, error) {
	var found direntInfo
	ok := false
	err := fs.scanDir(dir, func(d direntInfo) bool {
		if d.name == name {
			found = d
			ok = true
			return true
		}
		return false
	})
	return found, ok, err
}

// resolve walks p component by component from the root directory. The root
// path itself resolves to a synthetic entry describing the root directory.
func (fs *FS) resolve(p vfs.SafePath) (direntInfo, error) {
	comps := p.Components()
	if len(comps) == 0 {
		return direntInfo{
			name:         "",
			attr:         attrDirectory,
			firstCluster: fs.rootDirStart(),
			ino:          deriveIno(0, 0),
		}, nil
	}
	dir := fs.rootDirStart()
	var info direntInfo
	for i, name := range comps {
		if err := checkFATName(name); err != nil {
			return direntInfo{}, err
		}
		found, ok, err := fs.findEntry(dir, name)
		if err != nil {
			return direntInfo{}, err
		}
		if !ok {
			return direntInfo{}, fmt.Errorf("resolve %q: %w", p.String(), vfs.ErrNotFound)
		}
		if i < len(comps)-1 {
			if !found.isDir() {
				return direntInfo{}, fmt.Errorf("resolve %q: %w", p.String(), vfs.ErrNotFound)
			}
			dir = found.firstCluster
		}
		info = found
	}
	return info, nil
}

// resolveParent resolves p's parent directory and validates p's own leaf
// name, without requiring the leaf to already exist.
func (fs *FS) resolveParent(p vfs.SafePath) (parentCluster uint32, leaf string, err error) {
	if p.IsRoot() {
		return 0, "", fmt.Errorf("%w: the share root has no parent", vfs.ErrDenied)
	}
	leaf = p.Name()
	if nerr := checkFATName(leaf); nerr != nil {
		return 0, "", nerr
	}
	parentInfo, err := fs.resolve(p.Parent())
	if err != nil {
		return 0, "", err
	}
	if !parentInfo.isDir() && !p.Parent().IsRoot() {
		return 0, "", fmt.Errorf("resolve %q: %w", p.String(), vfs.ErrNotFound)
	}
	cluster := fs.rootDirStart()
	if !p.Parent().IsRoot() {
		cluster = parentInfo.firstCluster
	}
	return cluster, leaf, nil
}

// findInsertPoint is the byte offset of the first free (0x00) slot in dir,
// or the byte just past the end of its current region if every slot is in
// use.
func (fs *FS) findInsertPoint(dir uint32) (int64, error) {
	pos := int64(-1)
	err := fs.scanRawEntries(dir, func(e rawDirEntry, off int64) bool {
		if e[0] == direntFree {
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
	return fs.dirRegionSize(dir)
}

// appendEntries writes entries into dir starting at the current insertion
// point and re-terminates the directory with a fresh end marker. The whole
// batch's room is secured before any byte is written: a cluster-chain
// directory grows to fit in one step, and the FAT12/FAT16 fixed root is
// refused outright with ErrNoSpaceOnVolume rather than left with a
// half-written entry run when it does not fit.
func (fs *FS) appendEntries(dir *uint32, entries []rawDirEntry) (int64, error) {
	insertAt, err := fs.findInsertPoint(*dir)
	if err != nil {
		return 0, err
	}
	need := int64(len(entries)+1) * 32
	if *dir == fatFixedRoot {
		if insertAt+need > fs.fixedRootSize() {
			return 0, ErrNoSpaceOnVolume
		}
	} else if _, err := fs.ensureExtent(dir, insertAt+need); err != nil {
		return 0, err
	}
	for i, e := range entries {
		if err := fs.dirWriteAt(dir, insertAt+int64(i)*32, e[:]); err != nil {
			return 0, err
		}
	}
	var marker rawDirEntry
	if err := fs.dirWriteAt(dir, insertAt+int64(len(entries))*32, marker[:]); err != nil {
		return 0, err
	}
	return insertAt, nil
}

// deleteEntries marks every slot in [entryStart, entryEnd) deleted, freeing
// the whole long-name run together with its short entry in one call.
func (fs *FS) deleteEntries(dir uint32, entryStart, entryEnd int64) error {
	for off := entryStart; off < entryEnd; off += 32 {
		var b [1]byte
		b[0] = direntDeleted
		if err := fs.dirWriteAt(&dir, off, b[:]); err != nil {
			return err
		}
	}
	return nil
}

// buildEntry lays out a short entry's fixed fields; the caller fills in the
// name-dependent bytes (the 11-byte short name field) separately.
func buildEntry(shortName [11]byte, attr byte, cluster uint32, size uint32, mtimeNs int64) rawDirEntry {
	var e rawDirEntry
	copy(e[0:11], shortName[:])
	e[11] = attr
	date, tm := nsToFATTime(mtimeNs)
	binary.LittleEndian.PutUint16(e[14:16], tm)
	binary.LittleEndian.PutUint16(e[16:18], date)
	binary.LittleEndian.PutUint16(e[18:20], date)
	binary.LittleEndian.PutUint16(e[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[22:24], tm)
	binary.LittleEndian.PutUint16(e[24:26], date)
	binary.LittleEndian.PutUint16(e[26:28], uint16(cluster&0xFFFF))
	binary.LittleEndian.PutUint32(e[28:32], size)
	return e
}

// createEntry mints the long-name run and short-entry alias for name and
// appends them to dir.
func (fs *FS) createEntry(dir uint32, name string, attr byte, cluster uint32, size uint32, mtimeNs int64) error {
	existing, err := fs.collectShortNames(dir)
	if err != nil {
		return err
	}
	shortName, err := generateShortName(name, existing)
	if err != nil {
		return err
	}
	entries := buildLFNEntries(name, shortName)
	entries = append(entries, buildEntry(shortName, attr, cluster, size, mtimeNs))
	_, err = fs.appendEntries(&dir, entries)
	return err
}

// nsToFATTime converts UTC nanoseconds to FAT's local-time, 2-second
// resolution date and time fields, clamped to the range FAT can represent.
func nsToFATTime(ns int64) (date, tm uint16) {
	t := time.Unix(0, ns).Local()
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, t.Location())
	}
	if t.Year() > 2107 {
		t = time.Date(2107, 12, 31, 23, 59, 58, 0, t.Location())
	}
	sec := t.Second()
	if sec > 58 {
		sec = 58
	}
	hour := mustNarrow[uint16](t.Hour(), "hour")
	minute := mustNarrow[uint16](t.Minute(), "minute")
	halfSec := mustNarrow[uint16](sec/2, "half-second")
	tm = hour<<11 | minute<<5 | halfSec
	year := mustNarrow[uint16](t.Year()-1980, "FAT year")
	month := mustNarrow[uint16](int(t.Month()), "month")
	day := mustNarrow[uint16](t.Day(), "day")
	date = year<<9 | month<<5 | day
	return date, tm
}

// fatTimeToNs is nsToFATTime's inverse, always returning UTC nanoseconds
// normalized from the local time FAT stored.
func fatTimeToNs(date, tm uint16) int64 {
	year := 1980 + int(date>>9)
	month := int((date >> 5) & 0xF)
	day := int(date & 0x1F)
	if month < 1 {
		month = 1
	}
	if day < 1 {
		day = 1
	}
	hour := int(tm >> 11)
	minute := int((tm >> 5) & 0x3F)
	second := int(tm&0x1F) * 2
	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local)
	return t.UnixNano()
}

// Dirent is one entry ReadDir hands back.
type Dirent struct {
	Name  string
	IsDir bool
	Ino   uint64
}

// StatInfo is what Stat reports about one entry.
type StatInfo struct {
	IsDir   bool
	Size    uint64
	MtimeNs int64
	Ino     uint64
}

// Stat resolves p and reports its kind, size and modification time. Ino
// follows entryIno: an entry holding content reports its first cluster, and
// one holding none reports an inode derived from where its directory entry
// sits, so no two entries in a volume ever share an inode.
func (fs *FS) Stat(p vfs.SafePath) (StatInfo, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	info, err := fs.resolve(p)
	if err != nil {
		return StatInfo{}, err
	}
	return StatInfo{
		IsDir:   info.isDir(),
		Size:    uint64(info.size),
		MtimeNs: info.mtimeNs,
		Ino:     info.ino,
	}, nil
}

// ReadDir lists dir's entries, "." and ".." and the volume label excluded.
func (fs *FS) ReadDir(p vfs.SafePath) ([]Dirent, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	info, err := fs.resolve(p)
	if err != nil {
		return nil, err
	}
	if !info.isDir() {
		return nil, fmt.Errorf("read dir %q: %w", p.String(), vfs.ErrNotFound)
	}
	dir := fs.rootDirStart()
	if !p.IsRoot() {
		dir = info.firstCluster
	}
	var entries []Dirent
	err = fs.scanDir(dir, func(d direntInfo) bool {
		if d.name == "." || d.name == ".." {
			return false
		}
		entries = append(entries, Dirent{Name: d.name, IsDir: d.isDir(), Ino: d.ino})
		return false
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// Mkdir creates an empty directory at p, populated with "." and "..", the
// latter pointing at cluster 0 when the parent is the volume root, which is
// the FAT32 convention for "this directory's parent is not addressable by a
// real cluster number."
func (fs *FS) Mkdir(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	if _, ok, ferr := fs.findEntry(parent, leaf); ferr != nil {
		return ferr
	} else if ok {
		return fmt.Errorf("mkdir %q: %w", p.String(), vfs.ErrExists)
	}
	clusters, err := fs.allocateClusters(1)
	if err != nil {
		return err
	}
	newCluster := clusters[0]
	now := fs.clk.Nanos()
	parentForDotDot := parent
	if p.Parent().IsRoot() {
		parentForDotDot = 0
	}
	dotName := packShortName(".", "")
	dotDotName := packShortName("..", "")
	entries := []rawDirEntry{
		buildEntry(dotName, attrDirectory, newCluster, 0, now),
		buildEntry(dotDotName, attrDirectory, parentForDotDot, 0, now),
	}
	for i, e := range entries {
		if err := fs.writeExtent(&newCluster, int64(i)*32, e[:]); err != nil {
			return err
		}
	}
	var marker rawDirEntry
	if err := fs.writeExtent(&newCluster, int64(len(entries))*32, marker[:]); err != nil {
		return err
	}
	if err := fs.createEntry(parent, leaf, attrDirectory, newCluster, 0, now); err != nil {
		return err
	}
	return fs.flushFSInfo()
}

// Rmdir removes an empty directory. A directory holding anything besides
// "." and ".." is refused, never emptied on the caller's behalf.
func (fs *FS) Rmdir(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if p.IsRoot() {
		return fmt.Errorf("rmdir: %w", vfs.ErrDenied)
	}
	parent, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(parent, leaf)
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
	err = fs.scanDir(entry.firstCluster, func(d direntInfo) bool {
		if d.name == "." || d.name == ".." {
			return false
		}
		empty = false
		return true
	})
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("rmdir %q: %w", p.String(), vfs.ErrNotEmpty)
	}
	if err := fs.freeChain(entry.firstCluster); err != nil {
		return err
	}
	if err := fs.deleteEntries(parent, entry.entryStart, entry.shortOffset+32); err != nil {
		return err
	}
	return fs.flushFSInfo()
}

// CreateFile creates an empty file at p, refusing if anything already
// occupies the name.
func (fs *FS) CreateFile(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	if _, ok, err := fs.findEntry(parent, leaf); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("create %q: %w", p.String(), vfs.ErrExists)
	}
	return fs.createEntry(parent, leaf, attrArchive, 0, 0, fs.clk.Nanos())
}

// Remove deletes a file. A directory at p is refused with ErrIsDirectory,
// the mirror of Rmdir's refusal of a plain file.
func (fs *FS) Remove(p vfs.SafePath) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(parent, leaf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("remove %q: %w", p.String(), vfs.ErrNotFound)
	}
	if entry.isDir() {
		return fmt.Errorf("remove %q: %w", p.String(), vfs.ErrIsDirectory)
	}
	if err := fs.freeChain(entry.firstCluster); err != nil {
		return err
	}
	if err := fs.deleteEntries(parent, entry.entryStart, entry.shortOffset+32); err != nil {
		return err
	}
	return fs.flushFSInfo()
}

// ReadFile streams a file's whole content to w.
func (fs *FS) ReadFile(p vfs.SafePath, w io.Writer) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	info, err := fs.resolve(p)
	if err != nil {
		return err
	}
	if info.isDir() {
		return fmt.Errorf("read %q: %w", p.String(), vfs.ErrIsDirectory)
	}
	return fs.streamFile(info.firstCluster, int64(info.size), w)
}

func (fs *FS) streamFile(start uint32, size int64, w io.Writer) error {
	const chunk = 256 << 10
	buf := make([]byte, chunk)
	for off := int64(0); off < size; off += chunk {
		n := size - off
		if n > chunk {
			n = chunk
		}
		if err := fs.readExtent(start, off, buf[:n]); err != nil {
			return err
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return err
		}
	}
	return nil
}

// stagingSuffix mints a random name suffix for a durable write's staging
// entry, so two concurrent writes aimed at the same destination never
// collide on the same staging name even though this driver serializes them
// behind one mutex per filesystem: a caller holding two FS instances open on
// the same container file is a misuse this suffix does not have to prevent,
// but costs nothing to guard against anyway.
func stagingSuffix() []byte {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("vault: system randomness unavailable: " + err.Error())
	}
	return b
}

// WriteFileStaged writes r's entire content into the FAT filesystem under a
// staging name in dest's own parent directory, then renames it onto dest,
// which is what makes the write atomic from a reader's point of view: dest
// either still names its old content or already names the new content in
// full, never a partial write.
//
// noClobber refuses with vfs.ErrExists rather than replacing an existing
// dest.
func (fs *FS) WriteFileStaged(dest vfs.SafePath, r io.Reader, noClobber bool, mtimeNs int64) (bool, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, leaf, err := fs.resolveParent(dest)
	if err != nil {
		return false, err
	}
	existingEntry, exists, err := fs.findEntry(parent, leaf)
	if err != nil {
		return false, err
	}
	if exists && noClobber {
		return false, fmt.Errorf("write %q: %w", dest.String(), vfs.ErrExists)
	}
	if exists && existingEntry.isDir() {
		return false, fmt.Errorf("write %q: %w", dest.String(), vfs.ErrIsDirectory)
	}

	stagingName := fmt.Sprintf(".stowstage-%x", stagingSuffix())
	if cerr := fs.createEntry(parent, stagingName, attrArchive, 0, 0, mtimeNs); cerr != nil {
		return false, cerr
	}
	stagingEntry, ok, err := fs.findEntry(parent, stagingName)
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
			if written+int64(n) > maxFATFileSize {
				cleanupErr := errors.Join(fs.freeChain(cluster), fs.deleteEntries(parent, stagingEntry.entryStart, stagingEntry.shortOffset+32))
				return false, errors.Join(ErrFileTooLarge, cleanupErr)
			}
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
	if err := fs.updateShortEntry(parent, stagingEntry.shortOffset, func(e *rawDirEntry) {
		setEntryCluster(e, cluster)
		setEntrySize(e, mustNarrow[uint32](written, "staged file size"))
		setEntryTime(e, mtimeNs)
	}); err != nil {
		return false, err
	}

	if exists {
		if err := fs.freeChain(existingEntry.firstCluster); err != nil {
			return false, err
		}
		if err := fs.deleteEntries(parent, existingEntry.entryStart, existingEntry.shortOffset+32); err != nil {
			return false, err
		}
	}
	if err := fs.renameEntry(parent, stagingName, parent, leaf); err != nil {
		return false, err
	}
	return exists, fs.flushFSInfo()
}

// setEntryCluster patches a short entry's first-cluster field in place.
func setEntryCluster(e *rawDirEntry, cluster uint32) {
	binary.LittleEndian.PutUint16(e[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[26:28], uint16(cluster&0xFFFF))
}

// setEntrySize patches a short entry's file-size field in place.
func setEntrySize(e *rawDirEntry, size uint32) {
	binary.LittleEndian.PutUint32(e[28:32], size)
}

// setEntryTime patches a short entry's write time in place.
func setEntryTime(e *rawDirEntry, mtimeNs int64) {
	date, tm := nsToFATTime(mtimeNs)
	binary.LittleEndian.PutUint16(e[22:24], tm)
	binary.LittleEndian.PutUint16(e[24:26], date)
}

// updateShortEntry rewrites the 32 bytes at entryOff in dir through patch.
func (fs *FS) updateShortEntry(dir uint32, entryOff int64, patch func(e *rawDirEntry)) error {
	var e rawDirEntry
	if err := fs.dirReadAt(dir, entryOff, e[:]); err != nil {
		return err
	}
	patch(&e)
	d := dir
	return fs.dirWriteAt(&d, entryOff, e[:])
}

// SetModTime patches p's write time.
func (fs *FS) SetModTime(p vfs.SafePath, mtimeNs int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if p.IsRoot() {
		return fmt.Errorf("set mod time: %w", vfs.ErrDenied)
	}
	parent, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(parent, leaf)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("set mod time %q: %w", p.String(), vfs.ErrNotFound)
	}
	return fs.updateShortEntry(parent, entry.shortOffset, func(e *rawDirEntry) {
		setEntryTime(e, mtimeNs)
	})
}

// Truncate resizes a file, zero-filling any newly exposed bytes when
// growing and freeing whatever it no longer needs when shrinking.
func (fs *FS) Truncate(p vfs.SafePath, newSize uint64) error {
	if newSize > maxFATFileSize {
		return ErrFileTooLarge
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, leaf, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	entry, ok, err := fs.findEntry(parent, leaf)
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
	clusterSize := uint64(fs.bytesPerCluster())
	oldSize := uint64(entry.size)
	switch {
	case newSize == 0:
		if err := fs.freeChain(cluster); err != nil {
			return err
		}
		cluster = 0
	case newSize < oldSize:
		keep := mustNarrow[int]((newSize+clusterSize-1)/clusterSize, "cluster count after truncate")
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
	if err := fs.updateShortEntry(parent, entry.shortOffset, func(e *rawDirEntry) {
		setEntryCluster(e, cluster)
		setEntrySize(e, mustNarrow[uint32](newSize, "truncated file size"))
	}); err != nil {
		return err
	}
	return fs.flushFSInfo()
}

// renameEntry moves the logical entry named fromName in fromDir to toName in
// toDir, by rewriting its long-name run and short entry alias in the
// destination directory and deleting the original slots. The content
// clusters are untouched; only the directory metadata naming them moves.
func (fs *FS) renameEntry(fromDir uint32, fromName string, toDir uint32, toName string) error {
	entry, ok, err := fs.findEntry(fromDir, fromName)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("rename: source vanished: %w", vfs.ErrNotFound)
	}
	if err := fs.createEntry(toDir, toName, entry.attr, entry.firstCluster, entry.size, entry.mtimeNs); err != nil {
		return err
	}
	if entry.isDir() && toDir != fromDir {
		if err := fs.fixDotDot(entry.firstCluster, toDir); err != nil {
			return err
		}
	}
	return fs.deleteEntries(fromDir, entry.entryStart, entry.shortOffset+32)
}

// fixDotDot repoints a moved directory's ".." entry at its new parent,
// cluster 0 meaning the volume root, matching Mkdir's own convention.
func (fs *FS) fixDotDot(dirCluster uint32, newParent uint32) error {
	target := newParent
	if newParent == fs.rootDirStart() {
		target = 0
	}
	dotDotName := packShortName("..", "")
	found := false
	var updateErr error
	err := fs.scanRawEntries(dirCluster, func(e rawDirEntry, off int64) bool {
		if e[0] == direntFree {
			return true
		}
		var key [11]byte
		copy(key[:], e[0:11])
		if key == dotDotName {
			found = true
			updateErr = fs.updateShortEntry(dirCluster, off, func(entry *rawDirEntry) {
				setEntryCluster(entry, target)
			})
			return true
		}
		return false
	})
	if err != nil {
		return err
	}
	if updateErr != nil {
		return updateErr
	}
	if !found {
		return fmt.Errorf("rename: directory at cluster %d missing ..: %w", dirCluster, vfs.ErrNotFound)
	}
	return nil
}

// Rename moves the entry at from to to, within this FAT filesystem.
// noReplace refuses with vfs.ErrExists when to already names something.
func (fs *FS) Rename(from, to vfs.SafePath, noReplace bool) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if from.IsRoot() || to.IsRoot() {
		return fmt.Errorf("rename: %w", vfs.ErrDenied)
	}
	fromParent, fromLeaf, err := fs.resolveParent(from)
	if err != nil {
		return err
	}
	toParent, toLeaf, err := fs.resolveParent(to)
	if err != nil {
		return err
	}
	if existing, ok, err := fs.findEntry(toParent, toLeaf); err != nil {
		return err
	} else if ok {
		if noReplace {
			return fmt.Errorf("rename %q: %w", to.String(), vfs.ErrExists)
		}
		if existing.isDir() {
			return fmt.Errorf("rename %q: %w", to.String(), vfs.ErrIsDirectory)
		}
		if err := fs.freeChain(existing.firstCluster); err != nil {
			return err
		}
		if err := fs.deleteEntries(toParent, existing.entryStart, existing.shortOffset+32); err != nil {
			return err
		}
	}
	if err := fs.renameEntry(fromParent, fromLeaf, toParent, toLeaf); err != nil {
		return err
	}
	return fs.flushFSInfo()
}
