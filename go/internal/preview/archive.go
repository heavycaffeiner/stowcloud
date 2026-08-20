//go:build linux

package preview

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// Archive listing.
//
// Listing a zip reads the archive's central directory, not its contents.
// Nothing is extracted to list, so a zip bomb costs the directory parse and
// nothing else.
//
// This runs in the parent rather than the worker, and that is deliberate: it
// parses a directory structure rather than decoding image data, and putting it
// in the worker would mean passing a whole archive across the socket. The
// parser is bounded and fuzzed instead, which is the appropriate control for a
// structure parser that does not allocate per byte.

// ErrNotArchive is a file that is not a zip this build can read.
var ErrNotArchive = errors.New("preview: not a readable archive")

// ArchiveEntry is one member, as the central directory describes it.
type ArchiveEntry struct {
	Name  string
	Size  uint64
	IsDir bool
	// Compressed is what the member occupies inside the archive, which is what
	// makes a compression ratio visible to a caller that wants to refuse one.
	Compressed uint64
	ModTimeNs  int64
}

// ArchiveListing is what a zip holds.
type ArchiveListing struct {
	Entries []ArchiveEntry
	// Truncated reports that the entry cap cut the listing, so a caller can
	// say so rather than presenting a partial archive as a whole one.
	Truncated bool
	// TotalUncompressed is the sum over the entries listed. A caller compares
	// it against the archive's own size to see a bomb before extracting.
	TotalUncompressed uint64
}

// ListArchive reads the central directory of a zip.
//
// r is read through pread, so nothing is loaded to list: archive/zip seeks to
// the directory at the end of the file and reads only that.
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
		// Cancellation is checked per entry here rather than per directory as
		// in the walk: an archive's directory is already in memory, so the
		// loop is bounded by the entry count and the check is cheap beside it.
		if err := ctx.Err(); err != nil {
			return ArchiveListing{}, err
		}
		if len(out.Entries) >= limits.ArchiveEntriesListed {
			// Truncating rather than refusing: a caller can still show what is
			// there, and the flag stops it looking complete.
			out.Truncated = true
			break
		}

		name := f.Name
		// A member name is attacker-chosen and is only ever displayed here,
		// never used to open anything. It is still cleaned, because a name
		// carrying a traversal or a control character is a name a client would
		// render or, worse, hand to its own extractor.
		if !safeArchiveName(name) {
			continue
		}

		isDir := strings.HasSuffix(name, "/")
		e := ArchiveEntry{
			Name:       name,
			Size:       f.UncompressedSize64,
			Compressed: f.CompressedSize64,
			IsDir:      isDir,
		}
		if mt := f.Modified; !mt.IsZero() {
			e.ModTimeNs = mt.UnixNano()
		}
		out.Entries = append(out.Entries, e)
		out.TotalUncompressed += f.UncompressedSize64
	}
	return out, nil
}

// safeArchiveName reports whether a member name is one worth showing.
//
// The name is never opened by this build, so this is not a path-safety check
// standing between an attacker and the filesystem. It is a display filter: a
// client rendering the listing, or extracting from it, must not be handed a
// name that escapes a directory or carries control characters.
func safeArchiveName(name string) bool {
	if name == "" || len(name) > 4096 {
		return false
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	// A drive letter or a UNC prefix is the Windows shape of an absolute path.
	if len(name) >= 2 && name[1] == ':' {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] == 0x7f {
			return false
		}
	}
	return true
}
