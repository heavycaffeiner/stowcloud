//go:build linux && compat_nc

package nc

import (
	"net/url"
	"strings"
)

// The compat WebDAV path layout.
//
// The WebDAV package owns the protocol. This translates the compat URL layout
// onto the paths the core understands, and names the couple of collections
// that have no core equivalent.

// FutureFileName is the virtual child of an upload collection that the
// assembling request targets. It always "exists": it is the name the client
// moves onto the destination to finish an upload.
const FutureFileName = ".file"

// TargetKind is what a compat WebDAV path addresses.
type TargetKind int

const (
	// TargetNone is a path this layout does not name.
	TargetNone TargetKind = iota
	// TargetRoot is the root collection, which clients probe during
	// discovery.
	TargetRoot
	// TargetFiles is a file or folder in a user's tree.
	TargetFiles
	// TargetUploadHome is the upload collection's parent, where only creating
	// a child is meaningful.
	TargetUploadHome
	// TargetUploadFolder is one in-flight upload.
	TargetUploadFolder
	// TargetUploadChunk is one member of one.
	TargetUploadChunk
	// TargetPrincipalRoot and TargetPrincipal are the minimal principal stubs
	// clients probe.
	TargetPrincipalRoot
	TargetPrincipal
	// TargetTrashRoot is the one flat trash collection the protocol has, over
	// however many per-share trashes the caller can reach.
	TargetTrashRoot
	// TargetTrashEntry is one deleted item.
	TargetTrashEntry
	// TargetTrashRestore is the destination that means "put it back". The
	// leaf is ignored: the core restores to the path recorded in the entry,
	// which is what the user means.
	TargetTrashRestore
)

// DavTarget is a parsed compat WebDAV path.
type DavTarget struct {
	Kind TargetKind
	// User is the account the path names, which is empty for the legacy alias
	// that always means the caller's own tree.
	User string
	// Path is the file path, for a files target.
	Path string
	// TID and Name identify an upload and one of its members.
	TID  string
	Name string
	// Entry names one trash item.
	Entry string
}

// ParseDavPath reads a compat WebDAV path.
//
// Percent-decoding happens per segment, after splitting, so an encoded
// separator can never introduce a segment boundary. That is the classic way a
// path-mapping layer is walked out of its root, and doing it in the other
// order is the bug this avoids.
func ParseDavPath(uriPath string) DavTarget {
	rest, ok := strings.CutPrefix(uriPath, "/remote.php/")
	if !ok {
		// Some deployments and clients keep the index prefix.
		rest, ok = strings.CutPrefix(uriPath, "/index.php/remote.php/")
		if !ok {
			return DavTarget{}
		}
	}

	segs := strings.Split(rest, "/")
	if len(segs) == 0 {
		return DavTarget{}
	}

	switch segs[0] {
	case "webdav":
		// The legacy alias for the caller's own tree.
		path, ok := decodeJoin(segs[1:])
		if !ok {
			return DavTarget{}
		}
		return DavTarget{Kind: TargetFiles, Path: path}

	case "dav":
		return parseDavKind(segs[1:])
	}
	return DavTarget{}
}

func parseDavKind(segs []string) DavTarget {
	if len(segs) == 0 || segs[0] == "" {
		return DavTarget{Kind: TargetRoot}
	}

	switch segs[0] {
	case "files":
		user, rest, ok := userAnd(segs[1:])
		if !ok {
			return DavTarget{}
		}
		path, ok := decodeJoin(rest)
		if !ok {
			return DavTarget{}
		}
		return DavTarget{Kind: TargetFiles, User: user, Path: path}

	case "uploads":
		user, rest, ok := userAnd(segs[1:])
		if !ok {
			return DavTarget{}
		}
		if len(rest) == 0 || rest[0] == "" {
			return DavTarget{Kind: TargetUploadHome, User: user}
		}
		tid, ok := decodeSeg(rest[0])
		if !ok {
			return DavTarget{}
		}
		if len(rest) == 1 || rest[1] == "" {
			return DavTarget{Kind: TargetUploadFolder, User: user, TID: tid}
		}
		// No nesting below a member name: the reference forbids creating a
		// collection there, so a deeper path is not a path this names.
		if len(rest) > 2 {
			return DavTarget{}
		}
		name, ok := decodeSeg(rest[1])
		if !ok {
			return DavTarget{}
		}
		return DavTarget{Kind: TargetUploadChunk, User: user, TID: tid, Name: name}

	case "trashbin":
		return parseTrash(segs[1:])

	case "principals":
		if len(segs) < 2 || segs[1] == "" {
			return DavTarget{Kind: TargetPrincipalRoot}
		}
		if segs[1] != "users" || len(segs) < 3 {
			return DavTarget{Kind: TargetPrincipalRoot}
		}
		user, ok := decodeSeg(segs[2])
		if !ok || user == "" {
			return DavTarget{}
		}
		return DavTarget{Kind: TargetPrincipal, User: user}
	}
	return DavTarget{}
}

func parseTrash(segs []string) DavTarget {
	user, rest, ok := userAnd(segs)
	if !ok {
		return DavTarget{}
	}
	// The collection above the trash is not addressable on its own, and no
	// client asks for it.
	if len(rest) == 0 || rest[0] == "" {
		return DavTarget{}
	}

	switch rest[0] {
	case "trash":
		// One client sends the same URL with a trailing separator, and both
		// forms are the same collection.
		if len(rest) == 1 || rest[1] == "" {
			return DavTarget{Kind: TargetTrashRoot, User: user}
		}
		if len(rest) > 2 && rest[2] != "" {
			return DavTarget{}
		}
		entry, ok := decodeSeg(rest[1])
		if !ok {
			return DavTarget{}
		}
		return DavTarget{Kind: TargetTrashEntry, User: user, Entry: entry}

	case "restore":
		return DavTarget{Kind: TargetTrashRestore, User: user}
	}
	return DavTarget{}
}

// userAnd takes the account segment off the front.
func userAnd(segs []string) (user string, rest []string, ok bool) {
	if len(segs) == 0 {
		return "", nil, false
	}
	user, ok = decodeSeg(segs[0])
	if !ok || user == "" {
		return "", nil, false
	}
	return user, segs[1:], true
}

// decodeSeg decodes one segment.
//
// A segment that decodes to something containing a separator is refused rather
// than accepted: that is the encoded-separator case, and accepting it would
// let a client introduce a boundary the split already decided.
func decodeSeg(s string) (string, bool) {
	out, err := url.PathUnescape(s)
	if err != nil {
		return "", false
	}
	if strings.ContainsRune(out, '/') {
		return "", false
	}
	return out, true
}

// decodeJoin decodes each segment and rejoins them.
//
// A traversal segment is refused here rather than left to the path layer. The
// layer would refuse it too, but this is a malformed request and saying so is
// the better answer than a permission error.
func decodeJoin(segs []string) (string, bool) {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "" {
			continue
		}
		seg, ok := decodeSeg(s)
		if !ok {
			return "", false
		}
		if seg == "." || seg == ".." {
			return "", false
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/"), true
}
