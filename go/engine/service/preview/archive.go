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

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
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
const maxArchiveNameBytes = 4096

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

	var out ArchiveListing
	for _, f := range zr.File {
		// Cancellation is polled per entry rather than per directory as in a
		// filesystem walk. An archive's directory already sits in memory, so the
		// loop is bounded by the entry count and the check costs little beside
		// it.
		if cerr := ctx.Err(); cerr != nil {
			return ArchiveListing{}, cerr
		}
		if len(out.Entries) >= limits.ArchiveEntriesListed {
			// Truncated rather than rejected, so a caller can still display what
			// exists while the flag prevents it appearing complete.
			out.Truncated = true
			break
		}

		name := f.Name
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
