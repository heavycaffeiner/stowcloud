package vault

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// fatForbiddenChars are the bytes a FAT directory entry, long or short,
// cannot hold. This is not the same table vfs.SafePath enforces: vfs targets
// a Linux filesystem, which tolerates '*', '?' and '"' in a name, so a name
// that passed vfs validation still has to clear this narrower table before
// the FAT driver can store it.
const fatForbiddenChars = "\"*/:<>?\\|"

// checkFATName refuses a name this build cannot encode into a FAT directory
// entry: a character FAT reserves, or a code point above U+FFFF, which
// cannot be stored in a single UCS-2 long-name slot without a surrogate
// pair.
//
// The FAT driver runs this on every component it is about to create,
// because vfs.SafePath already validated the name for a Linux filesystem
// and knows nothing about FAT's narrower alphabet.
func checkFATName(name string) error {
	if name == "" {
		return fmt.Errorf("fat: empty name: %w", vfs.ErrInvalidName)
	}
	for _, r := range name {
		if strings.ContainsRune(fatForbiddenChars, r) {
			return fmt.Errorf("fat: %q: contains %q, which FAT reserves: %w", name, r, vfs.ErrInvalidName)
		}
		if r > 0xFFFF {
			return fmt.Errorf("fat: %q: contains %q above U+FFFF, which a UCS-2 long-name slot cannot hold: %w", name, r, vfs.ErrInvalidName)
		}
	}
	// A trailing dot or space is stripped by Windows and by this driver's
	// own short-name generator; refusing it up front means a listing never
	// shows a name that would come back different after a rename.
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("fat: %q: trailing dot or space: %w", name, vfs.ErrInvalidName)
	}
	return nil
}

// shortNameCharset is every byte an 8.3 short name may hold verbatim.
// Anything else collapses to '_' when this driver invents an alias.
const shortNameCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&'()-@^_`{}~"

// splitBaseExt splits a long name into the part before its last dot and the
// part after. A leading dot (a dotfile) is not a separator: "gitignore"
// hides in the base and the entry gets no extension, matching how Windows
// itself treats a name with no non-leading dot.
func splitBaseExt(name string) (base, ext string) {
	trimmed := strings.TrimLeft(name, ".")
	leadingDots := len(name) - len(trimmed)
	if idx := strings.LastIndexByte(trimmed, '.'); idx >= 0 {
		return name[:leadingDots+idx], trimmed[idx+1:]
	}
	return name, ""
}

// cleanShortComponent uppercases ASCII letters and folds everything the
// short-name charset does not hold to '_', which is what Windows itself
// does when it mints an alias for a name it cannot store verbatim.
func cleanShortComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		if r < 0x80 && strings.ContainsRune(shortNameCharset, r) {
			if _, err := b.WriteRune(r); err != nil {
				panicInfallibleWrite(err)
			}
		} else if r != ' ' && r != '.' {
			if err := b.WriteByte('_'); err != nil {
				panicInfallibleWrite(err)
			}
		}
	}
	return b.String()
}

// packShortName lays base and ext into the fixed 11-byte 8.3 field,
// space-padded, which is the on-disk form of a short directory entry name.
func packShortName(base, ext string) [11]byte {
	var out [11]byte
	for i := range out {
		out[i] = ' '
	}
	copy(out[:8], base)
	copy(out[8:11], ext)
	return out
}

// shortNameCandidate produces the numeric-tail alias for attempt n, the same
// scheme Windows uses: the base shrinks as the suffix grows so the whole
// thing always fits eight characters.
func shortNameCandidate(cleanBase, cleanExt string, n int) (base, ext string) {
	if cleanBase == "" {
		cleanBase = "FSCTNR"
	}
	if len(cleanExt) > 3 {
		cleanExt = cleanExt[:3]
	}
	suffix := "~" + strconv.Itoa(n)
	baseLen := 8 - len(suffix)
	if baseLen < 1 {
		baseLen = 1
	}
	if len(cleanBase) > baseLen {
		cleanBase = cleanBase[:baseLen]
	}
	return cleanBase + suffix, cleanExt
}

// maxShortNameAttempts bounds the numeric-tail search. A directory with more
// than this many colliding basenames is pathological input, not a case this
// driver has to serve, and refusing it outright beats looping forever over
// an attacker-sized directory.
const maxShortNameAttempts = 999_999

// existingShortNames is satisfied by the directory scan, and kept as an
// interface here so the generator has no dependency on directory layout.
type existingShortNames interface {
	hasShortName(packed [11]byte) bool
}

// generateShortName mints a short-name alias for longName that collides with
// no entry existingShortNames already lists, using the classic NAME~N
// numeric-tail scheme.
func generateShortName(longName string, existing existingShortNames) ([11]byte, error) {
	base, ext := splitBaseExt(longName)
	cleanBase := cleanShortComponent(base)
	cleanExt := cleanShortComponent(ext)
	for n := 1; n <= maxShortNameAttempts; n++ {
		b, e := shortNameCandidate(cleanBase, cleanExt, n)
		packed := packShortName(b, e)
		if !existing.hasShortName(packed) {
			return packed, nil
		}
	}
	return [11]byte{}, fmt.Errorf("fat: %q: exhausted short name aliases", longName)
}

// lfnChecksum is the Microsoft-defined checksum of an 8.3 short name, stored
// in every long-name entry so a reader can tell a long-name run apart from
// one left behind by a different, unrelated short entry after a crash.
func lfnChecksum(shortName [11]byte) byte {
	var sum byte
	for _, c := range shortName {
		if sum&1 != 0 {
			sum = 0x80 + (sum >> 1) + c
		} else {
			sum = (sum >> 1) + c
		}
	}
	return sum
}

// lfnUnitsPerEntry is how many UTF-16 code units one VFAT long-name
// directory entry holds: 5 in the first field, 6 in the second, 2 in the
// third.
const lfnUnitsPerEntry = 13

// encodeLFNUnits turns a validated long name into the UTF-16 code units the
// directory entries carry, including the NUL terminator but not the 0xFFFF
// padding, which depends on where the last entry's boundary falls.
func encodeLFNUnits(name string) []uint16 {
	units := utf16.Encode([]rune(name))
	return append(units, 0)
}

// buildLFNEntries lays out the long-name entries for name in the order they
// belong on disk: the entry holding the tail of the name first, carrying the
// last-logical-entry bit, down to ordinal 1 immediately before the short
// entry.
func buildLFNEntries(name string, shortName [11]byte) []rawDirEntry {
	units := encodeLFNUnits(name)
	count := (len(units) + lfnUnitsPerEntry - 1) / lfnUnitsPerEntry
	checksum := lfnChecksum(shortName)
	entries := make([]rawDirEntry, count)
	for i := range count {
		start := i * lfnUnitsPerEntry
		chunk := make([]uint16, lfnUnitsPerEntry)
		for j := range chunk {
			chunk[j] = 0xFFFF
		}
		for j := range lfnUnitsPerEntry {
			if start+j >= len(units) {
				break
			}
			chunk[j] = units[start+j]
		}
		var e rawDirEntry
		ordByte := byte(i + 1)
		if i == count-1 {
			ordByte |= 0x40
		}
		e[0] = ordByte
		putUTF16Field(e[1:11], chunk[0:5])
		e[11] = 0x0F
		e[12] = 0
		e[13] = checksum
		putUTF16Field(e[14:26], chunk[5:11])
		e[26] = 0
		e[27] = 0
		putUTF16Field(e[28:32], chunk[11:13])
		entries[count-1-i] = e
	}
	return entries
}

func putUTF16Field(dst []byte, units []uint16) {
	for i, u := range units {
		binary.LittleEndian.PutUint16(dst[2*i:2*i+2], u)
	}
}

func getUTF16Field(src []byte) []uint16 {
	units := make([]uint16, len(src)/2)
	for i := range units {
		units[i] = uint16(src[2*i]) | uint16(src[2*i+1])<<8
	}
	return units
}

// decodeLFNChunk extracts the up-to-13 code units a single long-name entry
// carries, stopping at the first NUL or 0xFFFF padding unit.
func decodeLFNChunk(e rawDirEntry) []uint16 {
	units := make([]uint16, 0, lfnUnitsPerEntry)
	units = append(units, getUTF16Field(e[1:11])...)
	units = append(units, getUTF16Field(e[14:26])...)
	units = append(units, getUTF16Field(e[28:32])...)
	for i, u := range units {
		if u == 0 || u == 0xFFFF {
			return units[:i]
		}
	}
	return units
}

// shortNameToString reformats a packed 8.3 field for display, honoring the
// NTRes lowercase bits VFAT uses to remember that a base or extension typed
// in lowercase was stored losslessly as a short-only name.
func shortNameToString(packed [11]byte, ntRes byte) string {
	base := strings.TrimRight(string(packed[:8]), " ")
	ext := strings.TrimRight(string(packed[8:11]), " ")
	if packed[0] == 0x05 {
		base = "\xE5" + base[1:]
	}
	if ntRes&0x08 != 0 {
		base = strings.ToLower(base)
	}
	if ntRes&0x10 != 0 {
		ext = strings.ToLower(ext)
	}
	if ext == "" {
		return base
	}
	return base + "." + ext
}
