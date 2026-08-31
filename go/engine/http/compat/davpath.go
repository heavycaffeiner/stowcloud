//go:build linux && compat_nc

// The vendor URL layouts, over the same decoder the native DAV mount uses.
//
// One decoder and not a second: a path parsed one way for the native mount and
// another way here is two answers to the same security question.
package compat

import (
	"errors"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
)

// The refusals a caller distinguishes.
var (
	// ErrNotCompatPath reports a path outside the vendor prefixes.
	ErrNotCompatPath = errors.New("not a compatibility path")
	// ErrUnknownLayout reports a vendor prefix with a shape nothing serves.
	ErrUnknownLayout = errors.New("an unknown compatibility layout")
	// ErrBadTransferID reports an unusable upload transfer id.
	ErrBadTransferID = errors.New("an unusable transfer id")
)

// Layout names which vendor URL shape a request matched.
type Layout uint8

const (
	// LayoutUnset is the zero value and never names a real request.
	LayoutUnset Layout = iota
	// LayoutFiles is the per-user file tree.
	LayoutFiles
	// LayoutLegacy is the older flat WebDAV root.
	LayoutLegacy
	// LayoutUploads is a chunked transfer's collection.
	LayoutUploads
	// LayoutTrash is the flattened trash collection.
	LayoutTrash
	// LayoutPrincipals is the principal stub tree.
	LayoutPrincipals
)

// String is the layout's name in a diagnostic.
func (l Layout) String() string {
	switch l {
	case LayoutFiles:
		return "files"
	case LayoutLegacy:
		return "legacy"
	case LayoutUploads:
		return "uploads"
	case LayoutTrash:
		return "trash"
	case LayoutPrincipals:
		return "principals"
	case LayoutUnset:
		return "unset"
	default:
		return "unknown"
	}
}

// DavRequest is a parsed vendor path.
type DavRequest struct {
	// Layout is which shape matched.
	Layout Layout
	// User is the username segment the URL carried, or empty.
	//
	// Recorded and never used to select a principal. The caller's own tree is
	// the authenticated one, so a client naming another user reaches its own
	// files rather than that user's.
	User string
	// Path is the resource path inside the layout.
	Path []string
	// Transfer is the upload transfer id, for the uploads layout.
	Transfer string
	// Member is the chunk or assembly member name, for the uploads layout.
	Member string
}

// The prefixes a vendor request may arrive under. Both spellings, because a
// deployment behind a rewriting proxy sees one and a direct client the other.
//
//nolint:gochecknoglobals // a lookup table, read-only after init.
var compatPrefixes = []string{"remote.php", "index.php/remote.php"}

// ParseDavPath recognises a vendor URL.
//
// The whole path goes through the shared split-before-decode helper first, so
// an encoded separator or a dot segment is refused here exactly as it is on
// the native mount.
func ParseDavPath(raw string) (DavRequest, error) {
	segs, err := dav.SplitPath(raw)
	if err != nil {
		return DavRequest{}, err
	}

	rest, ok := stripPrefix(segs)
	if !ok {
		return DavRequest{}, ErrNotCompatPath
	}
	if len(rest) == 0 {
		return DavRequest{}, ErrUnknownLayout
	}

	switch rest[0] {
	case "webdav":
		return DavRequest{Layout: LayoutLegacy, Path: rest[1:]}, nil

	case "dav":
		return parseDavSubtree(rest[1:])

	default:
		return DavRequest{}, ErrUnknownLayout
	}
}

// stripPrefix removes a vendor prefix, reporting whether one matched.
func stripPrefix(segs []string) ([]string, bool) {
	for _, prefix := range compatPrefixes {
		parts := strings.Split(prefix, "/")
		if len(segs) < len(parts) {
			continue
		}
		matched := true
		for i, want := range parts {
			if segs[i] != want {
				matched = false
				break
			}
		}
		if matched {
			return segs[len(parts):], true
		}
	}
	return nil, false
}

// parseDavSubtree reads what follows the dav segment.
func parseDavSubtree(segs []string) (DavRequest, error) {
	if len(segs) == 0 {
		return DavRequest{}, ErrUnknownLayout
	}

	switch segs[0] {
	case "files":
		if len(segs) < 2 {
			return DavRequest{}, ErrUnknownLayout
		}
		return DavRequest{Layout: LayoutFiles, User: segs[1], Path: segs[2:]}, nil

	case "uploads":
		return parseUploads(segs[1:])

	case "trashbin":
		return parseTrash(segs[1:])

	case "principals":
		// The stub tree, which a client walks to discover itself. Any depth is
		// accepted because the handler answers a fixed document rather than
		// resolving anything under it.
		return DavRequest{Layout: LayoutPrincipals, Path: segs[1:]}, nil

	default:
		return DavRequest{}, ErrUnknownLayout
	}
}

// parseUploads reads an upload collection path.
func parseUploads(segs []string) (DavRequest, error) {
	if len(segs) < 2 {
		return DavRequest{}, ErrUnknownLayout
	}

	out := DavRequest{Layout: LayoutUploads, User: segs[0], Transfer: segs[1]}
	if err := ValidTransferID(out.Transfer); err != nil {
		return DavRequest{}, err
	}

	switch len(segs) {
	case 2:
		return out, nil
	case 3:
		out.Member = segs[2]
		return out, nil
	default:
		// Deeper nesting is refused rather than truncated to the first three:
		// truncating serves a path the client did not ask for.
		return DavRequest{}, ErrUnknownLayout
	}
}

// parseTrash reads a trash path.
func parseTrash(segs []string) (DavRequest, error) {
	if len(segs) < 2 || segs[1] != "trash" {
		return DavRequest{}, ErrUnknownLayout
	}
	if len(segs) > 3 {
		return DavRequest{}, ErrUnknownLayout
	}
	return DavRequest{Layout: LayoutTrash, User: segs[0], Path: segs[2:]}, nil
}

// The transfer id's bounds.
const (
	transferIDMin = 1
	transferIDMax = 128
)

// ValidTransferID reports whether a transfer id is usable.
//
// The id comes from the client, so it is a name in the caller's own namespace
// and never anything the server minted. Restricting the alphabet keeps it from
// becoming a path segment that means something else, and the dot spellings are
// excluded because they name a directory rather than a transfer.
func ValidTransferID(id string) error {
	if len(id) < transferIDMin || len(id) > transferIDMax {
		return ErrBadTransferID
	}
	if id == "." || id == ".." {
		return ErrBadTransferID
	}

	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return ErrBadTransferID
		}
	}
	return nil
}

// AssemblyMember is the member name a MOVE assembles a transfer through.
const AssemblyMember = ".file"

// IsAssembly reports whether an uploads member names the assembly target
// rather than a chunk.
func IsAssembly(member string) bool { return member == AssemblyMember }

// ChunkRange is the numbers a vendor upload collection accepts.
//
// One through ten thousand, which is what the reference client sends. The
// range is stated here and the parsing is not: chunk names go through the same
// function the native mount uses, so "00001" and "1" cannot mean one chunk on
// one mount and two on the other.
func ChunkRange() dav.ChunkRange {
	return dav.ChunkRange{Min: 1, Max: 10000}
}

// ParseChunk returns the number an uploads member denotes.
//
// The assembly member is not a chunk and is refused here: a caller asks
// IsAssembly first, and letting ".file" through as a number would make the
// error say the wrong thing.
func ParseChunk(member string) (int64, error) {
	return dav.ParseChunkName(member, ChunkRange())
}
