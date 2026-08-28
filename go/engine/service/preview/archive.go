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

// Archive listing: what is in this zip, without extracting it.
//
// Listing reads the archive's central directory, not its contents, so a 100 GB
// zip costs a directory read and a zip bomb costs the directory parse. Nothing
// here opens an entry's content.
//
// This runs in the parent rather than the worker, and that is deliberate: it
// parses a directory structure rather than decoding image data, and putting it
// in the worker would mean passing a whole archive across the socket. The
// parser is bounded and fuzzed instead, which is the appropriate control for a
// structure parser that does not allocate per byte.

// ErrNotArchive is a file that is not a zip this build can read.
var ErrNotArchive = errors.New("preview: not a readable archive")

// maxArchiveNameBytes bounds a member name before it is kept. A name is only
// ever displayed, so this is a bound on what a client is asked to render.
const maxArchiveNameBytes = 4096

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
	// Skipped counts members left out because their names cannot be shown
	// safely. Reported rather than silent for the same reason Truncated is: a
	// listing missing entries that does not admit it reads as an archive with
	// fewer files in it, so ten thousand entries and three unsafe ones reads
	// as exactly that.
	Skipped int
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
		// in a filesystem walk: an archive's directory is already in memory, so
		// the loop is bounded by the entry count and the check is cheap beside
		// it.
		if cerr := ctx.Err(); cerr != nil {
			return ArchiveListing{}, cerr
		}
		if len(out.Entries) >= limits.ArchiveEntriesListed {
			// Truncating rather than refusing: a caller can still show what is
			// there, and the flag stops it looking complete.
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

// safeArchiveName reports whether a member name is one worth showing.
//
// This is a display filter, not a path-traversal guard, and the distinction
// matters: the name is never opened by this build, so traversal is not the risk
// standing between an attacker and the filesystem. What is a risk is a control
// character or an absolute path in a name a client renders, or hands to its own
// extractor.
func safeArchiveName(name string) bool {
	if name == "" || len(name) > maxArchiveNameBytes {
		return false
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return false
	}
	// A drive letter or a UNC prefix is the Windows shape of an absolute path.
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
