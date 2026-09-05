// Package vault serves a VeraCrypt container file as a Stowcloud share.
//
// A container is decrypted entirely in this process: volume.go and header.go
// hold the XTS layer that turns the container file into a plain
// random-access byte space, and this file plus fatdir.go and fatname.go hold
// a FAT32 driver over that byte space, because VeraCrypt formats a new
// container with FAT by default and this backend has to read what it wrote.
// Nothing here mounts anything; every byte the domain sees comes back
// through ordinary reads and writes issued by this process.
package vault

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// panicInfallibleWrite stops the process when a write into a hash.Hash or a
// strings.Builder fails. Both types document their Write methods as never
// returning an error, so a non-nil one here means that guarantee broke, and
// whatever this driver was building from the write would already be wrong.
func panicInfallibleWrite(err error) {
	panic("vault: an infallible write failed: " + err.Error())
}

// mustNarrow converts v to a narrower field this driver's own arithmetic
// has already bounded, either by a clamp right above the call or by the
// caller's documented range. A value that still did not fit is this
// driver's own bookkeeping gone wrong, not input a caller could usefully
// recover from.
func mustNarrow[To, From num.Integer](v From, what string) To {
	n, err := num.Narrow[To](v)
	if err != nil {
		panic("vault: " + what + " out of range: " + err.Error())
	}
	return n
}

// Device is the random-access byte space the FAT driver reads and writes.
// A *volume satisfies it over a VeraCrypt container's decrypted data area;
// a test satisfies it with a plain file or an in-memory buffer, so the FAT
// layer is testable without any encryption underneath it.
type Device interface {
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
}

// Bounds this driver enforces on a BPB it did not write itself, since a
// container's decrypted content is untrusted input the moment it did not
// come from this driver's own Format.
const (
	minBytesPerSector    = 512
	maxBytesPerSector    = 4096
	maxSectorsPerCluster = 128
	minFATCopies         = 1
	maxFATCopies         = 2
	minTotalClusters     = 1
	maxTotalClusters     = 0x0FFFFFF5
)

// maxFATFileSize is the largest size a FAT directory entry's 32-bit size
// field can hold. A write that would exceed it is refused rather than
// silently truncated into a wrong, smaller size on disk.
const maxFATFileSize = 0xFFFFFFFE

// ErrUnsupportedFilesystem names a decrypted volume this driver refuses to
// read: FAT12, FAT16, or a BPB with a value out of bounds for any FAT32
// filesystem this driver could safely walk.
var ErrUnsupportedFilesystem = errors.New("vault: unsupported or invalid filesystem")

// fatEOC is the end-of-chain marker this driver writes. Any value at or
// above 0x0FFFFFF8 (once masked to 28 bits) reads back as end of chain,
// which is what a reader has to accept regardless of which exact value a
// foreign formatter chose.
const fatEOC = 0x0FFFFFFF

// fatBad marks a cluster a formatter found unusable. This driver never picks
// one during allocation, but has to recognize one if a foreign image has it.
const fatBad = 0x0FFFFFF7

// bpb32 is this driver's parsed view of the FAT32 BIOS Parameter Block.
type bpb32 struct {
	bytesPerSector    uint32
	sectorsPerCluster uint32
	reservedSectors   uint32
	numFATs           uint32
	fatSize           uint32 // sectors per one copy of the FAT
	totalSectors      uint32
	rootCluster       uint32
	fsInfoSector      uint32
	backupBootSector  uint32
	volumeID          uint32
	label             [11]byte
}

// FS is one mounted FAT32 filesystem over a Device.
//
// Every exported method takes mu for its entire body. A FAT filesystem has
// no concurrency story of its own: two goroutines each extending a cluster
// chain at the same time would hand out the same free cluster to both and
// corrupt the volume, so this driver never lets two operations touch the FAT
// tables or a directory at once.
type FS struct {
	mu sync.Mutex

	dev Device
	bpb bpb32

	dataStartSector uint32
	totalClusters   uint32

	freeCount uint32
	nextFree  uint32

	// clk supplies the timestamps Mkdir and CreateFile stamp into a fresh
	// entry when the caller does not already have one to hand (WriteFileStaged
	// and Truncate take mtimeNs from their own caller instead).
	clk clock.Clock
}

func (fs *FS) bytesPerCluster() uint32 { return fs.bpb.bytesPerSector * fs.bpb.sectorsPerCluster }

func (fs *FS) sectorOffset(sector uint32) int64 { return int64(sector) * int64(fs.bpb.bytesPerSector) }

// clusterOffset is the byte offset of cluster c's first sector. Cluster
// numbering starts at 2, the FAT convention that leaves 0 and 1 as reserved
// FAT entries.
func (fs *FS) clusterOffset(c uint32) int64 {
	return fs.sectorOffset(fs.dataStartSector + (c-2)*fs.bpb.sectorsPerCluster)
}

func (fs *FS) readAt(p []byte, off int64) error {
	n, err := fs.dev.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func (fs *FS) writeAt(p []byte, off int64) error {
	n, err := fs.dev.WriteAt(p, off)
	if n == len(p) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrShortWrite
}

func (fs *FS) writeCluster(c uint32, buf []byte) error {
	return fs.writeAt(buf, fs.clusterOffset(c))
}

// parseBPB validates and decodes the first sector of a candidate FAT32
// volume. sizeBytes is the actual size of the device backing it, so every
// count the header claims is checked against ground truth rather than
// trusted outright: this content arrived either from a container file this
// process did not write, or from a header field an attacker fully controls.
func parseBPB(sector []byte, sizeBytes uint64) (bpb32, error) {
	if len(sector) < 90 {
		return bpb32{}, fmt.Errorf("%w: boot sector too short", ErrUnsupportedFilesystem)
	}
	if sector[510] != 0x55 || sector[511] != 0xAA {
		return bpb32{}, fmt.Errorf("%w: missing boot sector signature", ErrUnsupportedFilesystem)
	}
	bps := uint32(binary.LittleEndian.Uint16(sector[11:13]))
	if bps < minBytesPerSector || bps > maxBytesPerSector || bps&(bps-1) != 0 {
		return bpb32{}, fmt.Errorf("%w: bytes per sector %d out of range", ErrUnsupportedFilesystem, bps)
	}
	spc := uint32(sector[13])
	if spc == 0 || spc > maxSectorsPerCluster || spc&(spc-1) != 0 {
		return bpb32{}, fmt.Errorf("%w: sectors per cluster %d out of range", ErrUnsupportedFilesystem, spc)
	}
	reserved := uint32(binary.LittleEndian.Uint16(sector[14:16]))
	if reserved < 1 {
		return bpb32{}, fmt.Errorf("%w: reserved sector count %d", ErrUnsupportedFilesystem, reserved)
	}
	numFATs := uint32(sector[16])
	if numFATs < minFATCopies || numFATs > maxFATCopies {
		return bpb32{}, fmt.Errorf("%w: %d FAT copies", ErrUnsupportedFilesystem, numFATs)
	}
	rootEntryCount := binary.LittleEndian.Uint16(sector[17:19])
	fatSize16 := binary.LittleEndian.Uint16(sector[22:24])
	if fatSize16 != 0 || rootEntryCount != 0 {
		return bpb32{}, fmt.Errorf("%w: FAT12/FAT16 boot sector, not FAT32", ErrUnsupportedFilesystem)
	}
	totalSectors16 := binary.LittleEndian.Uint16(sector[19:21])
	totalSectors32 := binary.LittleEndian.Uint32(sector[32:36])
	totalSectors := totalSectors32
	if totalSectors16 != 0 {
		totalSectors = uint32(totalSectors16)
	}
	if totalSectors == 0 {
		return bpb32{}, fmt.Errorf("%w: zero total sectors", ErrUnsupportedFilesystem)
	}
	if uint64(totalSectors)*uint64(bps) > sizeBytes {
		return bpb32{}, fmt.Errorf("%w: boot sector claims %d sectors, device holds %d bytes",
			ErrUnsupportedFilesystem, totalSectors, sizeBytes)
	}
	fatSize := binary.LittleEndian.Uint32(sector[36:40])
	if fatSize == 0 {
		return bpb32{}, fmt.Errorf("%w: zero FAT size", ErrUnsupportedFilesystem)
	}
	if uint64(fatSize)*uint64(numFATs)+uint64(reserved) > uint64(totalSectors) {
		return bpb32{}, fmt.Errorf("%w: FAT tables larger than the volume", ErrUnsupportedFilesystem)
	}
	rootCluster := binary.LittleEndian.Uint32(sector[44:48])
	if rootCluster < 2 {
		return bpb32{}, fmt.Errorf("%w: root cluster %d", ErrUnsupportedFilesystem, rootCluster)
	}
	fsInfoSector := uint32(binary.LittleEndian.Uint16(sector[48:50]))
	if fsInfoSector != 0 && fsInfoSector >= reserved {
		return bpb32{}, fmt.Errorf("%w: FSInfo sector outside the reserved area", ErrUnsupportedFilesystem)
	}
	backupBootSector := uint32(binary.LittleEndian.Uint16(sector[50:52]))
	if backupBootSector != 0 && backupBootSector >= reserved {
		return bpb32{}, fmt.Errorf("%w: backup boot sector outside the reserved area", ErrUnsupportedFilesystem)
	}
	volumeID := binary.LittleEndian.Uint32(sector[67:71])
	var label [11]byte
	copy(label[:], sector[71:82])

	dataStartSector := reserved + numFATs*fatSize
	if uint64(dataStartSector) > uint64(totalSectors) {
		return bpb32{}, fmt.Errorf("%w: no room left for a data area", ErrUnsupportedFilesystem)
	}
	totalClusters := (totalSectors - dataStartSector) / spc
	if totalClusters < minTotalClusters || totalClusters > maxTotalClusters {
		return bpb32{}, fmt.Errorf("%w: %d data clusters", ErrUnsupportedFilesystem, totalClusters)
	}
	if uint64(rootCluster) >= uint64(totalClusters)+2 {
		return bpb32{}, fmt.Errorf("%w: root cluster %d beyond the volume", ErrUnsupportedFilesystem, rootCluster)
	}

	return bpb32{
		bytesPerSector:    bps,
		sectorsPerCluster: spc,
		reservedSectors:   reserved,
		numFATs:           numFATs,
		fatSize:           fatSize,
		totalSectors:      totalSectors,
		rootCluster:       rootCluster,
		fsInfoSector:      fsInfoSector,
		backupBootSector:  backupBootSector,
		volumeID:          volumeID,
		label:             label,
	}, nil
}

// Mount reads and validates the boot sector of dev, sized sizeBytes, and
// brings up the free-cluster accounting FSInfo carries (or, when FSInfo
// reports "unknown," by scanning the FAT once).
func Mount(dev Device, sizeBytes uint64, clk clock.Clock) (*FS, error) {
	sector := make([]byte, 512)
	if err := (&FS{dev: dev}).readAt(sector, 0); err != nil {
		return nil, fmt.Errorf("vault: read boot sector: %w", err)
	}
	bpb, err := parseBPB(sector, sizeBytes)
	if err != nil {
		return nil, err
	}
	if clk == nil {
		clk = clock.System()
	}
	fs := &FS{
		dev:             dev,
		bpb:             bpb,
		dataStartSector: bpb.reservedSectors + bpb.numFATs*bpb.fatSize,
		totalClusters:   (bpb.totalSectors - (bpb.reservedSectors + bpb.numFATs*bpb.fatSize)) / bpb.sectorsPerCluster,
		clk:             clk,
	}
	if err := fs.loadFSInfo(); err != nil {
		return nil, err
	}
	return fs, nil
}

// Alive re-reads the boot sector and confirms it still parses, which is what
// a health probe needs: it proves the container is still there and still a
// filesystem this driver can walk, without touching any content.
func (fs *FS) Alive() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	sector := make([]byte, 512)
	if err := fs.readAt(sector, 0); err != nil {
		return fmt.Errorf("vault: read boot sector: %w", err)
	}
	size := uint64(fs.bpb.totalSectors) * uint64(fs.bpb.bytesPerSector)
	if _, err := parseBPB(sector, size); err != nil {
		return err
	}
	return nil
}

const (
	fsInfoLeadSig  = 0x41615252
	fsInfoStrucSig = 0x61417272
	fsInfoTrailSig = 0xAA550000
	fsInfoUnknown  = 0xFFFFFFFF
)

// loadFSInfo reads the free-cluster hint. A missing or unrecognized FSInfo
// sector, or one reporting "unknown," falls back to counting the FAT
// directly: this driver has to know its free space exactly, since Space
// reports it to a caller deciding whether an upload fits.
func (fs *FS) loadFSInfo() error {
	if fs.bpb.fsInfoSector == 0 {
		return fs.rescanFreeCount()
	}
	sector := make([]byte, fs.bpb.bytesPerSector)
	if err := fs.readAt(sector, fs.sectorOffset(fs.bpb.fsInfoSector)); err != nil {
		return fmt.Errorf("vault: read FSInfo: %w", err)
	}
	if binary.LittleEndian.Uint32(sector[0:4]) != fsInfoLeadSig ||
		binary.LittleEndian.Uint32(sector[484:488]) != fsInfoStrucSig ||
		binary.LittleEndian.Uint32(sector[508:512]) != fsInfoTrailSig {
		return fs.rescanFreeCount()
	}
	free := binary.LittleEndian.Uint32(sector[488:492])
	next := binary.LittleEndian.Uint32(sector[492:496])
	if free == fsInfoUnknown || free > fs.totalClusters {
		return fs.rescanFreeCount()
	}
	fs.freeCount = free
	if next == fsInfoUnknown || next < 2 || next >= fs.totalClusters+2 {
		next = 2
	}
	fs.nextFree = next
	return nil
}

// rescanFreeCount counts free clusters directly from the FAT, in bounded
// chunks so an oversized FAT costs disk time, never unbounded memory.
func (fs *FS) rescanFreeCount() error {
	const chunkClusters = 1 << 16
	buf := make([]byte, chunkClusters*4)
	free := uint32(0)
	base := fs.sectorOffset(fs.bpb.reservedSectors)
	remaining := fs.totalClusters + 2
	for start := uint32(0); start < remaining; start += chunkClusters {
		n := remaining - start
		if n > chunkClusters {
			n = chunkClusters
		}
		region := buf[:n*4]
		if err := fs.readAt(region, base+int64(start)*4); err != nil {
			return fmt.Errorf("vault: scan FAT for free clusters: %w", err)
		}
		for i := range n {
			cluster := start + i
			if cluster < 2 {
				continue
			}
			if binary.LittleEndian.Uint32(region[i*4:i*4+4])&0x0FFFFFFF == 0 {
				free++
			}
		}
	}
	fs.freeCount = free
	fs.nextFree = 2
	return nil
}

// flushFSInfo writes the current free-cluster accounting back, so the next
// mount does not have to rescan the whole FAT.
func (fs *FS) flushFSInfo() error {
	if fs.bpb.fsInfoSector == 0 {
		return nil
	}
	sector := make([]byte, fs.bpb.bytesPerSector)
	binary.LittleEndian.PutUint32(sector[0:4], fsInfoLeadSig)
	binary.LittleEndian.PutUint32(sector[484:488], fsInfoStrucSig)
	binary.LittleEndian.PutUint32(sector[488:492], fs.freeCount)
	binary.LittleEndian.PutUint32(sector[492:496], fs.nextFree)
	binary.LittleEndian.PutUint32(sector[508:512], fsInfoTrailSig)
	if err := fs.writeAt(sector, fs.sectorOffset(fs.bpb.fsInfoSector)); err != nil {
		return err
	}
	// The backup boot area mirrors the first three reserved sectors
	// starting at backupBootSector, so the backup FSInfo sits at
	// backupBootSector plus the primary FSInfo's own sector index. A
	// nonstandard layout with the FSInfo sector outside that three-sector
	// window has no backup copy to keep honest, and is left alone.
	if fs.bpb.backupBootSector != 0 && fs.bpb.fsInfoSector < 3 {
		backupOff := fs.sectorOffset(fs.bpb.backupBootSector + fs.bpb.fsInfoSector)
		if err := fs.writeAt(sector, backupOff); err != nil {
			return err
		}
	}
	return nil
}

// fatEntryOffset is the byte offset of cluster c's 4-byte entry in FAT copy
// index.
func (fs *FS) fatEntryOffset(index uint32, c uint32) int64 {
	return fs.sectorOffset(fs.bpb.reservedSectors+index*fs.bpb.fatSize) + int64(c)*4
}

// getFATEntry reads cluster c's entry from the first FAT copy, masked to the
// 28 bits FAT32 actually uses.
func (fs *FS) getFATEntry(c uint32) (uint32, error) {
	var buf [4]byte
	if err := fs.readAt(buf[:], fs.fatEntryOffset(0, c)); err != nil {
		return 0, fmt.Errorf("vault: read FAT entry %d: %w", c, err)
	}
	return binary.LittleEndian.Uint32(buf[:]) & 0x0FFFFFFF, nil
}

// setFATEntry writes cluster c's entry to every FAT copy, preserving each
// copy's own reserved top 4 bits rather than assuming they are zero.
func (fs *FS) setFATEntry(c uint32, value uint32) error {
	for i := range fs.bpb.numFATs {
		off := fs.fatEntryOffset(i, c)
		var buf [4]byte
		if err := fs.readAt(buf[:], off); err != nil {
			return fmt.Errorf("vault: read FAT entry %d for update: %w", c, err)
		}
		top := binary.LittleEndian.Uint32(buf[:]) & 0xF0000000
		binary.LittleEndian.PutUint32(buf[:], top|(value&0x0FFFFFFF))
		if err := fs.writeAt(buf[:], off); err != nil {
			return fmt.Errorf("vault: write FAT entry %d: %w", c, err)
		}
	}
	return nil
}

// isEOC reports whether a FAT entry value marks the end of a cluster chain.
func isEOC(v uint32) bool { return v >= 0x0FFFFFF8 }

// chainClusters lists every cluster in the chain starting at start, bounded
// by the volume's own cluster count so a corrupt or cyclic FAT cannot spin
// this loop forever.
func (fs *FS) chainClusters(start uint32) ([]uint32, error) {
	if start == 0 {
		return nil, nil
	}
	clusters := make([]uint32, 0, 8)
	c := start
	limit, nerr := num.Narrow[int](fs.totalClusters + 2)
	if nerr != nil {
		return nil, fmt.Errorf("vault: volume cluster count out of range: %w", nerr)
	}
	for {
		clusters = append(clusters, c)
		if len(clusters) > limit {
			return nil, fmt.Errorf("vault: cluster chain longer than the volume, probably cyclic")
		}
		next, err := fs.getFATEntry(c)
		if err != nil {
			return nil, err
		}
		if isEOC(next) {
			return clusters, nil
		}
		if next == 0 || next == fatBad || next >= fs.totalClusters+2 {
			return nil, fmt.Errorf("vault: cluster chain references invalid cluster %d", next)
		}
		c = next
	}
}

// allocateClusters hands out n fresh clusters linked into one chain ending
// in EOC, updating the free-cluster count as it goes. The scan starts at
// nextFree and wraps once, which is the classic "linear with wraparound"
// allocator FAT32 expects FSInfo's hint to support.
func (fs *FS) allocateClusters(n int) ([]uint32, error) {
	if n == 0 {
		return nil, nil
	}
	need, nerr := num.Narrow[uint32](n)
	if nerr != nil {
		return nil, fmt.Errorf("vault: allocate %d clusters: %w", n, nerr)
	}
	if need > fs.freeCount {
		return nil, ErrNoSpaceOnVolume
	}
	found := make([]uint32, 0, n)
	start := fs.nextFree
	if start < 2 || start >= fs.totalClusters+2 {
		start = 2
	}
	c := start
	for range fs.totalClusters {
		if len(found) >= n {
			break
		}
		entry, err := fs.getFATEntry(c)
		if err != nil {
			return nil, err
		}
		if entry == 0 {
			found = append(found, c)
		}
		c++
		if c >= fs.totalClusters+2 {
			c = 2
		}
	}
	if len(found) < n {
		return nil, ErrNoSpaceOnVolume
	}
	for i, cluster := range found {
		value := uint32(fatEOC)
		if i+1 < len(found) {
			value = found[i+1]
		}
		if err := fs.setFATEntry(cluster, value); err != nil {
			return nil, err
		}
	}
	fs.freeCount -= need
	fs.nextFree = c
	return found, nil
}

// ErrNoSpaceOnVolume is this driver's out-of-space sentinel, mapped to
// vfs.ErrNoSpace at the vfs.Root seam.
var ErrNoSpaceOnVolume = errors.New("vault: no free clusters left on the volume")

// ErrFileTooLarge means a write or a truncate asked for a size FAT's 32-bit
// size field cannot represent.
var ErrFileTooLarge = errors.New("vault: file size exceeds what FAT32 can address")

// extendChain allocates n more clusters and links them onto the end of the
// chain whose current last cluster is last.
func (fs *FS) extendChain(last uint32, n int) ([]uint32, error) {
	added, err := fs.allocateClusters(n)
	if err != nil {
		return nil, err
	}
	if err := fs.setFATEntry(last, added[0]); err != nil {
		return nil, err
	}
	return added, nil
}

// freeChain releases every cluster in the chain starting at start.
func (fs *FS) freeChain(start uint32) error {
	if start == 0 {
		return nil
	}
	clusters, err := fs.chainClusters(start)
	if err != nil {
		return err
	}
	for _, c := range clusters {
		if err := fs.setFATEntry(c, 0); err != nil {
			return err
		}
	}
	fs.freeCount += mustNarrow[uint32](len(clusters), "freed cluster count")
	return nil
}

// truncateChainAfter keeps the first keep clusters of the chain starting at
// start, marks the keep-th one EOC, and frees the rest. keep must be at
// least 1; truncating to zero clusters is freeChain's job.
func (fs *FS) truncateChainAfter(start uint32, keep int) error {
	clusters, err := fs.chainClusters(start)
	if err != nil {
		return err
	}
	if keep >= len(clusters) {
		return nil
	}
	if err := fs.setFATEntry(clusters[keep-1], fatEOC); err != nil {
		return err
	}
	freed := uint32(0)
	for _, c := range clusters[keep:] {
		if err := fs.setFATEntry(c, 0); err != nil {
			return err
		}
		freed++
	}
	fs.freeCount += freed
	return nil
}

// Space reports the FAT filesystem's own free space: the real limit a
// caller writing into this volume has to respect, not whatever the host
// filesystem underneath the container file happens to have left.
func (fs *FS) Space() (total, free uint64) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	bpc := uint64(fs.bytesPerCluster())
	return uint64(fs.totalClusters) * bpc, uint64(fs.freeCount) * bpc
}

// Sync flushes the free-cluster accounting to FSInfo. It does not sync the
// underlying device; the caller (vault.Root.Close, or a durable write that
// wants the container file synced) does that once, after every FS-level
// bookkeeping write it needs has already happened.
func (fs *FS) Sync() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.flushFSInfo()
}

// chooseSectorsPerCluster scales a fresh FAT32's cluster size to the volume
// size, the same ranges dosfstools itself uses for FAT32: small volumes get
// 512-byte clusters, which maximizes the cluster count (and so how finely
// this driver can track free space) for the modest volumes a container
// realistically is, and large volumes get bigger clusters to keep the FAT
// itself from becoming a large fraction of the volume.
func chooseSectorsPerCluster(sizeBytes uint64) uint32 {
	switch {
	case sizeBytes < 128<<20:
		return 1
	case sizeBytes < 8<<30:
		return 8
	case sizeBytes < 16<<30:
		return 16
	case sizeBytes < 32<<30:
		return 32
	default:
		return 64
	}
}

// computeFATSize finds the smallest FAT size, in sectors, that can address
// every cluster the remaining space yields once that many sectors are set
// aside for numFATs copies of it. The exact relationship is circular (the
// FAT's own size affects how much space is left for data, which affects how
// many clusters there are, which affects how large the FAT needs to be), so
// this starts from a closed-form estimate and corrects it until the fixed
// point holds exactly.
func computeFATSize(availSectors uint64, secPerClus uint32, numFATs uint32, bytesPerSector uint32) (fatSectors uint32, totalClusters uint32, err error) {
	bps := uint64(bytesPerSector)
	spc := uint64(secPerClus)
	nf := uint64(numFATs)
	entriesPerSector := bps / 4

	num := 4*availSectors + 8*spc
	den := bps*spc + 4*nf
	guess := (num + den - 1) / den
	if guess < 1 {
		guess = 1
	}
	for {
		if nf*guess >= availSectors {
			return 0, 0, fmt.Errorf("vault: volume too small to hold a FAT32 filesystem")
		}
		dataSectors := availSectors - nf*guess
		clusters := dataSectors / spc
		if clusters > maxTotalClusters {
			clusters = maxTotalClusters
		}
		needed := (clusters + 2 + entriesPerSector - 1) / entriesPerSector
		if needed <= guess {
			return uint32(guess), uint32(clusters), nil
		}
		guess = needed
	}
}

// zeroRegion writes n zero bytes at off, in bounded chunks.
func zeroRegion(dev Device, off int64, n int64) error {
	const chunk = 1 << 20
	buf := make([]byte, min(int64(chunk), n))
	for n > 0 {
		size := int64(len(buf))
		if n < size {
			size = n
		}
		if _, err := dev.WriteAt(buf[:size], off); err != nil {
			return err
		}
		off += size
		n -= size
	}
	return nil
}

// encodeBootSector lays out the 512-byte FAT32 boot sector Format writes,
// identically for the primary copy at sector 0 and the backup copy at
// backupBootSector. Every field narrower on disk than bpb's own uint32 is
// bounded before it goes in: Format's own values always fit, but a value
// that ever grew past the on-disk field's width would otherwise wrap into a
// different, silently wrong geometry rather than being refused.
func encodeBootSector(bpb bpb32) ([]byte, error) {
	sector := make([]byte, 512)
	sector[0], sector[1], sector[2] = 0xEB, 0x58, 0x90
	copy(sector[3:11], "STOWFAT ")
	bytesPerSector, err := num.Narrow[uint16](bpb.bytesPerSector)
	if err != nil {
		return nil, fmt.Errorf("vault: bytes per sector: %w", err)
	}
	binary.LittleEndian.PutUint16(sector[11:13], bytesPerSector)
	sectorsPerCluster, err := num.Narrow[byte](bpb.sectorsPerCluster)
	if err != nil {
		return nil, fmt.Errorf("vault: sectors per cluster: %w", err)
	}
	sector[13] = sectorsPerCluster
	reservedSectors, err := num.Narrow[uint16](bpb.reservedSectors)
	if err != nil {
		return nil, fmt.Errorf("vault: reserved sectors: %w", err)
	}
	binary.LittleEndian.PutUint16(sector[14:16], reservedSectors)
	numFATs, err := num.Narrow[byte](bpb.numFATs)
	if err != nil {
		return nil, fmt.Errorf("vault: number of FATs: %w", err)
	}
	sector[16] = numFATs
	// RootEntryCount, TotalSectors16 and FATSize16 all stay zero: that
	// triple is what marks this a FAT32 BPB rather than FAT12/FAT16.
	sector[21] = 0xF8 // fixed-disk media descriptor
	binary.LittleEndian.PutUint16(sector[24:26], 0x3F)
	binary.LittleEndian.PutUint16(sector[26:28], 0xFF)
	binary.LittleEndian.PutUint32(sector[32:36], bpb.totalSectors)
	binary.LittleEndian.PutUint32(sector[36:40], bpb.fatSize)
	binary.LittleEndian.PutUint32(sector[44:48], bpb.rootCluster)
	fsInfoSector, err := num.Narrow[uint16](bpb.fsInfoSector)
	if err != nil {
		return nil, fmt.Errorf("vault: FSInfo sector: %w", err)
	}
	binary.LittleEndian.PutUint16(sector[48:50], fsInfoSector)
	backupBootSector, err := num.Narrow[uint16](bpb.backupBootSector)
	if err != nil {
		return nil, fmt.Errorf("vault: backup boot sector: %w", err)
	}
	binary.LittleEndian.PutUint16(sector[50:52], backupBootSector)
	sector[64] = 0x80 // fixed disk
	sector[66] = 0x29 // boot signature, marks VolumeID/Label/FileSystemType present
	binary.LittleEndian.PutUint32(sector[67:71], bpb.volumeID)
	copy(sector[71:82], bpb.label[:])
	copy(sector[82:90], "FAT32   ")
	sector[510], sector[511] = 0x55, 0xAA
	return sector, nil
}

// Format writes a fresh FAT32 filesystem into dev, sized sizeBytes: boot
// sector and its backup, FSInfo and its backup, both FAT copies, and an
// empty root directory. The container's data area is otherwise left exactly
// as Create's caller filled it, which for a VeraCrypt container is random
// bytes: nothing outside the structures this function writes should look
// different from unallocated space.
func Format(dev Device, sizeBytes uint64) error {
	const bps = 512
	totalSectors := sizeBytes / bps
	if totalSectors > 0xFFFFFFFF {
		totalSectors = 0xFFFFFFFF
	}
	spc := chooseSectorsPerCluster(sizeBytes)
	const reserved = 32
	const numFATs = 2
	if totalSectors <= reserved {
		return fmt.Errorf("vault: volume too small to hold a FAT32 filesystem")
	}
	fatSize, totalClusters, err := computeFATSize(totalSectors-reserved, spc, numFATs, bps)
	if err != nil {
		return err
	}

	var volumeID [4]byte
	if _, rerr := rand.Read(volumeID[:]); rerr != nil {
		return fmt.Errorf("vault: generate volume id: %w", rerr)
	}

	bpb := bpb32{
		bytesPerSector:    bps,
		sectorsPerCluster: spc,
		reservedSectors:   reserved,
		numFATs:           numFATs,
		fatSize:           fatSize,
		totalSectors:      uint32(totalSectors),
		rootCluster:       2,
		fsInfoSector:      1,
		backupBootSector:  6,
		volumeID:          binary.LittleEndian.Uint32(volumeID[:]),
	}
	// "NO NAME    " is the standard dosfstools placeholder for an unlabeled
	// volume. A boot sector label with no matching volume-label entry in
	// the root directory is a mismatch fsck.fat flags and auto-corrects;
	// using the placeholder both filesystems agree means "no label" avoids
	// manufacturing that mismatch on every freshly formatted volume.
	copy(bpb.label[:], "NO NAME    ")

	fsys := &FS{
		dev:             dev,
		bpb:             bpb,
		dataStartSector: bpb.reservedSectors + bpb.numFATs*bpb.fatSize,
		totalClusters:   totalClusters,
	}

	boot, err := encodeBootSector(bpb)
	if err != nil {
		return err
	}
	if err := fsys.writeAt(boot, fsys.sectorOffset(0)); err != nil {
		return fmt.Errorf("vault: write boot sector: %w", err)
	}
	if err := fsys.writeAt(boot, fsys.sectorOffset(bpb.backupBootSector)); err != nil {
		return fmt.Errorf("vault: write backup boot sector: %w", err)
	}

	fatBytes := int64(bpb.fatSize) * int64(bps)
	for i := range bpb.numFATs {
		if err := zeroRegion(dev, fsys.sectorOffset(bpb.reservedSectors+i*bpb.fatSize), fatBytes); err != nil {
			return fmt.Errorf("vault: clear FAT copy %d: %w", i, err)
		}
	}
	fsys.freeCount = totalClusters
	fsys.nextFree = 3
	if err := fsys.setFATEntry(0, 0x0FFFFFF8); err != nil {
		return err
	}
	if err := fsys.setFATEntry(1, fatEOC); err != nil {
		return err
	}
	if err := fsys.setFATEntry(2, fatEOC); err != nil {
		return err
	}
	fsys.freeCount--

	rootBytes := make([]byte, fsys.bytesPerCluster())
	if err := fsys.writeCluster(2, rootBytes); err != nil {
		return fmt.Errorf("vault: clear root directory: %w", err)
	}

	return fsys.flushFSInfo()
}
