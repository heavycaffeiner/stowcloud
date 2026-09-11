//go:build linux

package preview

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/uniname"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
)

// Archive listing: reporting a zip's contents without extracting them.
//
// Listing consults the archive's central directory rather than its contents, so
// a 100 GB zip costs a directory read and a zip bomb costs a directory parse.
// Nothing here opens an entry's data.
//
// This executes in the parent rather than the worker, deliberately: it parses a
// directory structure instead of decoding image data, and relocating it to the
// worker would require shipping an entire archive across the socket. The parser
// is bounded and fuzzed instead, the appropriate control for a structure parser
// that allocates nothing per byte.

// ErrNotArchive reports a file that is not a zip this build can read.
var ErrNotArchive = errors.New("preview: not a readable archive")

// maxArchiveNameBytes bounds a member name before it is kept. A name is only
// ever displayed, so this is a bound on what a client is asked to render.
// Checked after decoding, not against the raw central-directory bytes: a
// legacy code page can grow under decode (one CP949 byte pair becomes three
// UTF-8 bytes), and the bound has to hold for what the client actually
// receives.
const maxArchiveNameBytes = 4096

// maxArchiveNameSampleBytes bounds the detection sample built from entries
// that need decoding. Detection accuracy saturates after a few dozen bytes,
// so this is generous relative to what the detector actually needs.
const maxArchiveNameSampleBytes = 1 << 16

// ArchiveEntry describes a single member as the central directory records it.
type ArchiveEntry struct {
	Name  string
	Size  uint64
	IsDir bool
	// Compressed gives the space the member occupies within the archive, which
	// is what exposes a compression ratio to a caller wishing to reject one.
	Compressed uint64
	ModTimeNs  int64
}

// ArchiveListing describes a zip's contents.
type ArchiveListing struct {
	Entries []ArchiveEntry
	// Truncated indicates the entry cap shortened the listing, letting a caller
	// disclose that instead of presenting a partial archive as complete.
	Truncated bool
	// Skipped counts members omitted because their names cannot be displayed
	// safely. It is reported rather than hidden for the same reason as
	// Truncated: a listing that silently drops entries reads as an archive
	// containing fewer files, so ten thousand entries with three unsafe ones
	// would appear as exactly that.
	Skipped int
	// TotalUncompressed sums the listed entries. A caller weighs it against the
	// archive's own size to spot a bomb before extracting.
	TotalUncompressed uint64
}

// archiveNameNeedsDecode reports whether a central directory entry's raw name
// bytes are something other than UTF-8: either the UTF-8 flag was clear, or
// Go's reader found the bytes not valid UTF-8 regardless of the flag.
func archiveNameNeedsDecode(f *zip.File) bool {
	return f.NonUTF8 || !utf8.ValidString(f.Name)
}

const (
	zipEndLength       = 22
	zipMaxComment      = 1<<16 - 1
	zip64LocatorLength = 20
	zip64EndLength     = 56
)

// validateArchiveDirectory rejects a central directory before archive/zip
// allocates one File value per declared member. The physical span from the
// declared directory start to the directory end is checked in addition to the
// format's declared byte and entry counts, so underreported metadata cannot
// make a large directory look small.
func validateArchiveDirectory(r io.ReaderAt, size int64) error {
	total, err := num.Narrow[uint64](size)
	if err != nil {
		return fmt.Errorf("%w: negative size", ErrNotArchive)
	}
	tailLength := int64(zipEndLength + zipMaxComment)
	if tailLength > size {
		tailLength = size
	}
	tail := make([]byte, int(tailLength))
	if _, err := r.ReadAt(tail, size-tailLength); err != nil {
		return fmt.Errorf("%w: reading the directory trailer: %v", ErrNotArchive, err)
	}

	var end []byte
	var endOffset int64
	for i := len(tail) - zipEndLength; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) != 0x06054b50 {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(tail[i+20:]))
		if i+zipEndLength+commentLength != len(tail) {
			continue
		}
		end = tail[i : i+zipEndLength]
		endOffset = size - tailLength + int64(i)
		break
	}
	if end == nil {
		return fmt.Errorf("%w: no end of central directory", ErrNotArchive)
	}

	entries := uint64(binary.LittleEndian.Uint16(end[10:]))
	directoryBytes := uint64(binary.LittleEndian.Uint32(end[12:]))
	directoryOffset := uint64(binary.LittleEndian.Uint32(end[16:]))
	directoryEndOffset := endOffset
	if entries == uint64(^uint16(0)) ||
		directoryBytes == uint64(^uint32(0)) ||
		directoryOffset == uint64(^uint32(0)) {
		if endOffset < zip64LocatorLength {
			return fmt.Errorf("%w: missing ZIP64 locator", ErrNotArchive)
		}
		var locator [zip64LocatorLength]byte
		if _, err := r.ReadAt(locator[:], endOffset-zip64LocatorLength); err != nil {
			return fmt.Errorf("%w: reading the ZIP64 locator: %v", ErrNotArchive, err)
		}
		if binary.LittleEndian.Uint32(locator[:]) != 0x07064b50 ||
			binary.LittleEndian.Uint32(locator[4:]) != 0 ||
			binary.LittleEndian.Uint32(locator[16:]) != 1 {
			return fmt.Errorf("%w: invalid ZIP64 locator", ErrNotArchive)
		}
		zip64Offset := binary.LittleEndian.Uint64(locator[8:])
		if size < zip64EndLength || zip64Offset > uint64(size-zip64EndLength) {
			return fmt.Errorf("%w: invalid ZIP64 directory offset", ErrNotArchive)
		}
		zip64Start, nerr := num.Narrow[int64](zip64Offset)
		if nerr != nil {
			return fmt.Errorf("%w: invalid ZIP64 directory offset", ErrNotArchive)
		}
		var zip64End [zip64EndLength]byte
		if _, rerr := r.ReadAt(zip64End[:], zip64Start); rerr != nil {
			return fmt.Errorf("%w: reading the ZIP64 directory: %v", ErrNotArchive, rerr)
		}
		recordSize := binary.LittleEndian.Uint64(zip64End[4:])
		locatorOffset := uint64(endOffset - zip64LocatorLength)
		if binary.LittleEndian.Uint32(zip64End[:]) != 0x06064b50 ||
			recordSize < 44 ||
			recordSize > total-zip64Offset-12 ||
			zip64Offset+12+recordSize != locatorOffset {
			return fmt.Errorf("%w: invalid ZIP64 directory", ErrNotArchive)
		}
		entries = binary.LittleEndian.Uint64(zip64End[32:])
		directoryBytes = binary.LittleEndian.Uint64(zip64End[40:])
		directoryOffset = binary.LittleEndian.Uint64(zip64End[48:])
		directoryEndOffset = zip64Start
	}

	if directoryOffset > uint64(directoryEndOffset) {
		return fmt.Errorf("%w: invalid central directory offset", ErrNotArchive)
	}
	physicalDirectoryBytes := uint64(directoryEndOffset) - directoryOffset
	if physicalDirectoryBytes > limits.ArchiveDirectoryBytes {
		return fmt.Errorf("%w: a %d-byte central directory exceeds the parser bound", ErrNotArchive, physicalDirectoryBytes)
	}
	if entries > limits.ArchiveEntriesParsed {
		return fmt.Errorf("%w: %d entries exceed the parser bound", ErrNotArchive, entries)
	}
	if directoryBytes > limits.ArchiveDirectoryBytes || directoryBytes > total {
		return fmt.Errorf("%w: a %d-byte central directory exceeds the parser bound", ErrNotArchive, directoryBytes)
	}
	if directoryBytes != physicalDirectoryBytes {
		return fmt.Errorf("%w: declared central directory size %d differs from its physical span %d",
			ErrNotArchive, directoryBytes, physicalDirectoryBytes)
	}
	if entries > 0 && directoryBytes < entries*46 {
		return fmt.Errorf("%w: the central directory is too short for %d entries", ErrNotArchive, entries)
	}
	if err := validateCentralDirectory(r, directoryOffset, uint64(directoryEndOffset), entries); err != nil {
		return err
	}
	return nil
}

func validateCentralDirectory(r io.ReaderAt, offset, end, declaredEntries uint64) error {
	var (
		header [46]byte
		actual uint64
	)
	for offset < end {
		remaining := end - offset
		if remaining < uint64(len(header)) {
			return fmt.Errorf("%w: trailing bytes in the central directory", ErrNotArchive)
		}
		at, nerr := num.Narrow[int64](offset)
		if nerr != nil {
			return fmt.Errorf("%w: a central directory offset is out of range", ErrNotArchive)
		}
		if _, err := r.ReadAt(header[:], at); err != nil {
			return fmt.Errorf("%w: reading a central directory header: %v", ErrNotArchive, err)
		}
		if binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return fmt.Errorf("%w: malformed central directory header", ErrNotArchive)
		}
		recordBytes := uint64(len(header)) +
			uint64(binary.LittleEndian.Uint16(header[28:])) +
			uint64(binary.LittleEndian.Uint16(header[30:])) +
			uint64(binary.LittleEndian.Uint16(header[32:]))
		if recordBytes > remaining {
			return fmt.Errorf("%w: a central directory record exceeds its span", ErrNotArchive)
		}
		offset += recordBytes
		actual++
		if actual > limits.ArchiveEntriesParsed {
			return fmt.Errorf("%w: %d entries exceed the parser bound", ErrNotArchive, actual)
		}
	}
	if actual != declaredEntries {
		return fmt.Errorf("%w: central directory declares %d entries but contains %d",
			ErrNotArchive, declaredEntries, actual)
	}
	return nil
}

// ListArchive parses a zip's central directory.
//
// r is accessed via pread, so listing loads nothing: archive/zip seeks to the
// directory at the file's end and reads only that.
func ListArchive(ctx context.Context, r io.ReaderAt, size int64) (ArchiveListing, error) {
	if size <= 0 {
		return ArchiveListing{}, fmt.Errorf("%w: an empty file", ErrNotArchive)
	}
	if err := validateArchiveDirectory(r, size); err != nil {
		return ArchiveListing{}, err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return ArchiveListing{}, fmt.Errorf("%w: %w", ErrNotArchive, err)
	}

	// A first pass concatenates the raw name bytes of every entry that needs
	// decoding into one sample, so the character encoding is identified once
	// for the whole archive rather than once per name: chardet's accuracy
	// scales with how much text it sees, and a lone entry name is close to the
	// shortest input it can say anything about. An archive where every entry
	// is already UTF-8 with its flag set, the ordinary case, builds no sample
	// and calls no detector.
	var sample []byte
	for _, f := range zr.File {
		if archiveNameNeedsDecode(f) && len(sample) < maxArchiveNameSampleBytes {
			sample = append(sample, f.Name...)
		}
	}
	var cs encoding.Encoding
	if len(sample) > 0 {
		// CP437 is the fallback because that is the encoding the zip format
		// specifies for an entry without the UTF-8 flag; the detector exists
		// only to catch the East Asian code pages that no zip header declares.
		cs = uniname.Charset(sample, charmap.CodePage437)
	}

	var (
		out  ArchiveListing
		seen int64
	)
	for _, f := range zr.File {
		// Cancellation is polled per entry rather than per directory as in a
		// filesystem walk. An archive's directory already sits in memory, so
		// the loop is bounded by the entry count and the check costs little
		// beside it.
		if cerr := ctx.Err(); cerr != nil {
			return ArchiveListing{}, cerr
		}
		if seen >= limits.ArchiveEntriesListed {
			// Truncated rather than rejected, so a caller can still display
			// what exists while the flag prevents it appearing complete.
			out.Truncated = true
			break
		}
		seen++

		// An entry that needed decoding goes through the sample's detected
		// encoding, or CP437 when the sample could not be placed; one that was
		// already UTF-8 only gets normalized to NFC, so a macOS-made archive
		// lists the same as everything else. Either way safeArchiveName runs on
		// the decoded name: that is the display-safety filter's job, and the
		// decoded name is what a client actually renders or forwards, not the
		// raw central-directory bytes.
		var name string
		if archiveNameNeedsDecode(f) {
			name = uniname.Decode([]byte(f.Name), cs)
		} else {
			name = uniname.Normalize(f.Name)
		}
		if !safeArchiveName(name) {
			out.Skipped++
			continue
		}

		e := ArchiveEntry{
			Name:       name,
			Size:       f.UncompressedSize64,
			Compressed: f.CompressedSize64,
			IsDir:      strings.HasSuffix(name, "/"),
		}
		if mt := f.Modified; !mt.IsZero() {
			e.ModTimeNs = mt.UnixNano()
		}
		out.Entries = append(out.Entries, e)
		if ^uint64(0)-out.TotalUncompressed < f.UncompressedSize64 {
			out.TotalUncompressed = ^uint64(0)
		} else {
			out.TotalUncompressed += f.UncompressedSize64
		}
	}
	return out, nil
}

// safeArchiveName reports whether a member name is fit to display.
//
// This filters for display and does not guard against path traversal, and the
// distinction matters: this build never opens the name, so traversal is not what
// stands between an attacker and the filesystem. The actual risk is a control
// character or an absolute path inside a name that a client renders or forwards
// to its own extractor.
func safeArchiveName(name string) bool {
	if name == "" || len(name) > maxArchiveNameBytes {
		return false
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return false
	}
	// Drive letters and UNC prefixes are how Windows spells an absolute path.
	if len(name) >= 2 && name[1] == ':' {
		return false
	}
	if slices.Contains(strings.Split(name, "/"), "..") {
		return false
	}
	for i := range len(name) {
		if name[i] < 0x20 || name[i] == 0x7f {
			return false
		}
	}
	return true
}
