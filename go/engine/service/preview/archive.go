//go:build linux

package preview

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
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
// that need decoding, the same way maxListed bounds the entries kept: without
// it, an archive of a million oddly-encoded entries would concatenate every
// one of their names before the first Charset call. Detection accuracy
// saturates after a few dozen bytes, so this is generous relative to what the
// detector actually needs.
const maxArchiveNameSampleBytes = 1 << 16

// archiveMaxListed is the live bound on how many entries a listing keeps, an
// operator's adjustment of the compiled-in default. A package-level atomic
// rather than a field on some archive-specific type, because ListArchive is a
// bare function with no service to hold one: the setting is process-wide, the
// same way the compiled-in constant it replaces was.
var archiveMaxListed atomic.Int64 //nolint:gochecknoglobals // ListArchive is a bare function with no service to hold this; the bound it replaces was a compiled-in constant, equally process-wide.

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

// SetMaxListed applies an operator's change to how many entries a listing
// keeps, without a restart. Zero or negative restores the compiled-in
// default, the same convention SetBounds uses elsewhere in this tree.
func SetMaxListed(n int) {
	if n <= 0 {
		n = limits.ArchiveEntriesListed
	}
	archiveMaxListed.Store(int64(n))
}

// maxListed is the bound ListArchive enforces: the operator's setting once
// one has been stored, the compiled-in default otherwise.
func maxListed() int64 {
	if n := archiveMaxListed.Load(); n > 0 {
		return n
	}
	return limits.ArchiveEntriesListed
}

// archiveNameNeedsDecode reports whether a central directory entry's raw name
// bytes are something other than UTF-8: either the UTF-8 flag was clear, or
// Go's reader found the bytes not valid UTF-8 regardless of the flag.
func archiveNameNeedsDecode(f *zip.File) bool {
	return f.NonUTF8 || !utf8.ValidString(f.Name)
}

// ListArchive parses a zip's central directory.
//
// r is accessed via pread, so listing loads nothing: archive/zip seeks to the
// directory at the file's end and reads only that.
func ListArchive(ctx context.Context, r io.ReaderAt, size int64) (ArchiveListing, error) {
	if size <= 0 {
		return ArchiveListing{}, fmt.Errorf("%w: an empty file", ErrNotArchive)
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

	var out ArchiveListing
	for _, f := range zr.File {
		// Cancellation is polled per entry rather than per directory as in a
		// filesystem walk. An archive's directory already sits in memory, so the
		// loop is bounded by the entry count and the check costs little beside
		// it.
		if cerr := ctx.Err(); cerr != nil {
			return ArchiveListing{}, cerr
		}
		if int64(len(out.Entries)) >= maxListed() {
			// Truncated rather than rejected, so a caller can still display what
			// exists while the flag prevents it appearing complete.
			out.Truncated = true
			break
		}

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
		out.TotalUncompressed += f.UncompressedSize64
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
