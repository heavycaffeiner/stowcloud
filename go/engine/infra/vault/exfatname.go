package vault

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// exfatForbiddenChars are the bytes an exFAT directory entry cannot hold.
// vfs.SafePath already refuses ':' and control bytes at path construction;
// this is the narrower table exFAT itself reserves on top of that, the
// same role fatname.go's fatForbiddenChars plays for FAT.
const exfatForbiddenChars = "\"*/:<>?\\|"

// checkExFatName refuses a name this build cannot encode into an exFAT
// directory entry set: a character exFAT reserves, a code point above
// U+FFFF (this driver's up-case table covers only the BMP), or a name
// longer than the 255 UTF-16 units the NameLength field can express.
//
// Unlike FAT, exFAT stores every name in full Unicode with no 8.3 alias to
// protect, so there is no trailing-dot-or-space rule to repeat here: vfs
// already refuses that at path construction.
func checkExFatName(name string) error {
	if name == "" {
		return fmt.Errorf("exfat: empty name: %w", vfs.ErrInvalidName)
	}
	units := 0
	for _, r := range name {
		if strings.ContainsRune(exfatForbiddenChars, r) {
			return fmt.Errorf("exfat: %q: contains %q, which exFAT reserves: %w", name, r, vfs.ErrInvalidName)
		}
		if r > 0xFFFF {
			return fmt.Errorf("exfat: %q: contains %q above U+FFFF, which a UTF-16 name entry cannot hold: %w", name, r, vfs.ErrInvalidName)
		}
		units++
	}
	if units > 255 {
		return fmt.Errorf("exfat: %q: name longer than exFAT's 255 UTF-16 unit limit: %w", name, vfs.ErrInvalidName)
	}
	return nil
}

// exEntrySetChecksum is the checksum exFAT stores in every File entry's
// SetChecksum field: a 16-bit rotate-and-add over the whole entry set,
// skipping the two checksum bytes themselves, which only the primary
// entry carries.
func exEntrySetChecksum(set []exRawEntry) uint16 {
	var sum uint16
	for i, e := range set {
		for j, b := range e {
			if i == 0 && (j == 2 || j == 3) {
				continue
			}
			sum = (sum<<15 | sum>>1) + uint16(b)
		}
	}
	return sum
}

// nameHash folds units through the up-case table and accumulates exFAT's
// own rotate-and-add hash over the resulting bytes, the same algorithm a
// real exFAT implementation uses both to fill a Stream Extension entry's
// NameHash field and to verify one it reads.
func (fs *ExFatFS) nameHash(units []uint16) uint16 {
	var hash uint16
	for _, u := range units {
		up := fs.upcaseRune(u)
		// The hash consumes the code unit one byte at a time, low then
		// high, so both truncations are the algorithm rather than a loss.
		hash = (hash<<15 | hash>>1) + (up & 0xff)
		hash = (hash<<15 | hash>>1) + (up >> 8)
	}
	return hash
}

// buildFileEntrySet lays out the File, Stream Extension and File Name
// entries for one directory entry, including its NameHash and its whole
// set's checksum, ready to append to a directory.
func (fs *ExFatFS) buildFileEntrySet(name string, attr uint16, cluster uint32, dataLength, validDataLength uint64, mtimeNs int64, noFatChain bool) ([]exRawEntry, error) {
	units := utf16.Encode([]rune(name))
	if len(units) == 0 || len(units) > 255 {
		return nil, fmt.Errorf("exfat: %q: invalid encoded name length: %w", name, vfs.ErrInvalidName)
	}
	nameEntries := (len(units) + 14) / 15
	secondaryCount := 1 + nameEntries
	set := make([]exRawEntry, secondaryCount+1)

	date, tm := nsToFATTime(mtimeNs)
	ts := uint32(tm) | uint32(date)<<16
	set[0][0] = exEntryFile
	set[0][1] = mustNarrow[byte](secondaryCount, "directory entry set secondary count")
	binary.LittleEndian.PutUint16(set[0][4:6], attr)
	binary.LittleEndian.PutUint32(set[0][8:12], ts)
	binary.LittleEndian.PutUint32(set[0][12:16], ts)
	binary.LittleEndian.PutUint32(set[0][16:20], ts)

	set[1][0] = exEntryStreamExt
	var flags byte
	if cluster != 0 {
		flags |= exFlagAllocationPossible
	}
	if noFatChain {
		flags |= exFlagNoFatChain
	}
	set[1][1] = flags
	set[1][3] = mustNarrow[byte](len(units), "encoded name length")
	binary.LittleEndian.PutUint16(set[1][4:6], fs.nameHash(units))
	binary.LittleEndian.PutUint64(set[1][8:16], validDataLength)
	binary.LittleEndian.PutUint32(set[1][20:24], cluster)
	binary.LittleEndian.PutUint64(set[1][24:32], dataLength)

	for i := range nameEntries {
		set[2+i][0] = exEntryFileName
		start := i * 15
		end := min(start+15, len(units))
		putUTF16Field(set[2+i][2:2+2*(end-start)], units[start:end])
	}

	checksum := exEntrySetChecksum(set)
	binary.LittleEndian.PutUint16(set[0][2:4], checksum)
	return set, nil
}

// decodeEntrySet validates and decodes a File entry together with its
// secondaries, already read off disk as a contiguous run starting at
// entryStart. A checksum, a name-hash or a structural mismatch is refused
// as ErrCorruptDirectory rather than read past: exFAT gives a reader the
// means to tell a well-formed entry set from a torn or forged one, and not
// using it would defeat the point of storing it.
func (fs *ExFatFS) decodeEntrySet(set []exRawEntry, entryStart int64) (exDirentInfo, error) {
	wantChecksum := binary.LittleEndian.Uint16(set[0][2:4])
	if exEntrySetChecksum(set) != wantChecksum {
		return exDirentInfo{}, fmt.Errorf("%w: entry set checksum at offset %d", ErrCorruptDirectory, entryStart)
	}
	if set[1][0] != exEntryStreamExt {
		return exDirentInfo{}, fmt.Errorf("%w: file entry at offset %d missing its stream extension", ErrCorruptDirectory, entryStart)
	}
	attr := binary.LittleEndian.Uint16(set[0][4:6])
	rawTs := binary.LittleEndian.Uint32(set[0][12:16])
	// The timestamp is a packed pair of 16-bit fields, date in the high
	// half and time in the low half.
	mtimeNs := fatTimeToNs(uint16(rawTs>>16), uint16(rawTs&0xffff))

	flags := set[1][1]
	nameLength := int(set[1][3])
	nameHashWant := binary.LittleEndian.Uint16(set[1][4:6])
	validDataLength := binary.LittleEndian.Uint64(set[1][8:16])
	firstCluster := binary.LittleEndian.Uint32(set[1][20:24])
	dataLength := binary.LittleEndian.Uint64(set[1][24:32])

	maxVolumeBytes := uint64(fs.bpb.clusterCount) * uint64(fs.bpb.bytesPerCluster())
	if err := exfatBounds(dataLength <= maxVolumeBytes,
		"entry at offset %d claims a %d-byte size larger than the volume", entryStart, dataLength); err != nil {
		return exDirentInfo{}, err
	}
	if err := exfatBounds(validDataLength <= dataLength,
		"entry at offset %d has a valid length larger than its own size", entryStart); err != nil {
		return exDirentInfo{}, err
	}
	if err := exfatBounds(firstCluster == 0 || (firstCluster >= 2 && uint64(firstCluster) < uint64(fs.bpb.clusterCount)+2),
		"entry at offset %d starts at cluster %d, beyond the volume", entryStart, firstCluster); err != nil {
		return exDirentInfo{}, err
	}

	nameEntries := (nameLength + 14) / 15
	if nameEntries != len(set)-2 {
		return exDirentInfo{}, fmt.Errorf("%w: name length %d does not match %d file-name entries at offset %d",
			ErrCorruptDirectory, nameLength, len(set)-2, entryStart)
	}
	units := make([]uint16, 0, nameLength)
	for i := range nameEntries {
		e := set[2+i]
		if e[0] != exEntryFileName {
			return exDirentInfo{}, fmt.Errorf("%w: expected a file-name entry at offset %d",
				ErrCorruptDirectory, entryStart+int64(2+i)*32)
		}
		units = append(units, getUTF16Field(e[2:32])...)
	}
	units = units[:nameLength]
	if fs.nameHash(units) != nameHashWant {
		return exDirentInfo{}, fmt.Errorf("%w: name hash mismatch at offset %d", ErrCorruptDirectory, entryStart)
	}

	return exDirentInfo{
		name:            string(utf16.Decode(units)),
		attr:            attr,
		firstCluster:    firstCluster,
		dataLength:      dataLength,
		validDataLength: validDataLength,
		noFatChain:      flags&exFlagNoFatChain != 0,
		mtimeNs:         mtimeNs,
		entryStart:      entryStart,
		secondaryCount:  len(set) - 1,
	}, nil
}

// decodeExFatLabel reads a Volume Label entry's character count and name
// field, exFAT's own encoding for the volume label FormatExFat leaves
// unset by default.
func decodeExFatLabel(e exRawEntry) string {
	n := int(e[1])
	if n > 11 {
		n = 11
	}
	units := getUTF16Field(e[2 : 2+2*n])
	return string(utf16.Decode(units))
}
