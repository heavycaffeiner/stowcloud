//go:build linux && compat_nc

package nc

import (
	"fmt"
	"net/url"
	"strings"
)

// The URL layouts the clients address, and the hrefs a response gives back.
//
// A request's own prefix is kept rather than normalised away. One client
// abandons a whole listing when an href does not begin with the path it asked
// for, so a response has to answer under the spelling the request used: the
// front-controller form and the clean form are both live, and the account
// segment inside the files layout is part of what the client compares.
//
// The account segment is dropped rather than checked. Resolution runs against
// the caller's own roots, so a name in the URL cannot widen what the
// credential allows, and a client that spells its own login differently from
// the account record (a different case, a percent-encoded character) would
// otherwise be refused for naming itself.

// The mount points, without the optional front-controller prefix.
const (
	davMount    = "/remote.php/dav"
	webdavMount = "/remote.php/webdav"
	frontPrefix = "/index.php"
	// AssemblyMember is the member a client moves to publish a chunked
	// transfer. It is a name the collection interprets, not a file.
	AssemblyMember = ".file"
)

// TargetKind is which of the vocabulary's trees a request addresses.
type TargetKind uint8

// The trees. Anything else is refused before a handler runs.
const (
	KindUnknown TargetKind = iota
	// KindRoot is the mount itself, which holds no resource: a client asks
	// about it to confirm the server speaks the protocol.
	KindRoot
	KindFiles
	KindUploads
	KindTrash
	KindPrincipals
	KindComments
	KindSystemtags
	KindVersions
)

// TrashOp is which half of the trash tree a request addresses.
type TrashOp uint8

// The two halves. A client lists and deletes under one and restores by moving
// into the other.
const (
	TrashNone TrashOp = iota
	TrashList
	TrashRestore
)

// Target is one parsed request path.
type Target struct {
	Kind TargetKind
	// Prefix is everything up to the resource: the mount, the tree and the
	// account segment, exactly as the request spelled them, with no trailing
	// slash. Every href in the response is built from it.
	Prefix string
	// User is the account segment the client used, empty in a layout that has
	// none. Carried for the response's benefit only.
	User string
	// Path is the decoded components below the tree.
	Path []string
	// Session and Member address the chunked upload collection.
	Session string
	Member  string
	// Trash names which half of the trash tree this is.
	Trash TrashOp
}

// ParseTarget reads a request path.
//
// The input is the escaped path, because a path component may hold a slash
// once decoded and splitting a decoded string would put that component in two.
// Reports false for a path outside every layout, which the caller answers as a
// refusal rather than guessing.
func ParseTarget(escapedPath string) (Target, bool) {
	p := collapseSlashes(escapedPath)
	p = strings.TrimPrefix(p, frontPrefix)

	if rest, ok := strings.CutPrefix(p, webdavMount); ok {
		comps, cerr := splitPath(rest)
		if cerr != nil {
			return Target{}, false
		}
		return Target{Kind: KindFiles, Prefix: prefixFor(escapedPath, webdavMount, 0), Path: comps}, true
	}

	rest, ok := strings.CutPrefix(p, davMount)
	if !ok {
		return Target{}, false
	}
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return Target{Kind: KindRoot, Prefix: prefixFor(escapedPath, davMount, 0)}, true
	}

	tree, tail, _ := strings.Cut(rest, "/")
	switch tree {
	case "files":
		return filesTarget(escapedPath, tail)
	case "uploads":
		return uploadsTarget(escapedPath, tail)
	case "trashbin":
		return trashTarget(escapedPath, tail)
	case "principals":
		return Target{Kind: KindPrincipals, Prefix: prefixFor(escapedPath, davMount, 1)}, true
	case "comments":
		return Target{Kind: KindComments, Prefix: prefixFor(escapedPath, davMount, 1)}, true
	case "systemtags", "systemtags-relations":
		return Target{Kind: KindSystemtags, Prefix: prefixFor(escapedPath, davMount, 1)}, true
	case "versions":
		return Target{Kind: KindVersions, Prefix: prefixFor(escapedPath, davMount, 1)}, true
	default:
		return Target{}, false
	}
}

// filesTarget reads the files layout, whose first segment names the account.
func filesTarget(escapedPath, tail string) (Target, bool) {
	account, rest, _ := strings.Cut(tail, "/")
	comps, err := splitPath(rest)
	if err != nil {
		return Target{}, false
	}
	user, uerr := url.PathUnescape(account)
	if uerr != nil {
		user = account
	}
	return Target{
		Kind:   KindFiles,
		Prefix: prefixFor(escapedPath, davMount, 2),
		User:   user,
		Path:   comps,
	}, true
}

// uploadsTarget reads the chunked upload collection: an account, a session and
// at most one member.
func uploadsTarget(escapedPath, tail string) (Target, bool) {
	account, rest, _ := strings.Cut(tail, "/")
	user, uerr := url.PathUnescape(account)
	if uerr != nil {
		user = account
	}
	session, member, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
	session = strings.TrimSuffix(session, "/")
	member = strings.TrimSuffix(member, "/")
	if strings.Contains(member, "/") {
		return Target{}, false
	}
	decodedSession, serr := url.PathUnescape(session)
	if serr != nil {
		return Target{}, false
	}
	decodedMember, merr := url.PathUnescape(member)
	if merr != nil {
		return Target{}, false
	}
	return Target{
		Kind:    KindUploads,
		Prefix:  prefixFor(escapedPath, davMount, 2),
		User:    user,
		Session: decodedSession,
		Member:  decodedMember,
	}, true
}

// trashTarget reads the trash tree: an account, then the listing half or the
// restore half.
func trashTarget(escapedPath, tail string) (Target, bool) {
	account, rest, _ := strings.Cut(tail, "/")
	user, uerr := url.PathUnescape(account)
	if uerr != nil {
		user = account
	}
	half, itemPath, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
	comps, err := splitPath(itemPath)
	if err != nil {
		return Target{}, false
	}
	t := Target{Kind: KindTrash, Prefix: prefixFor(escapedPath, davMount, 2), User: user, Path: comps}
	switch strings.TrimSuffix(half, "/") {
	case "trash":
		t.Trash = TrashList
	case "restore":
		t.Trash = TrashRestore
	case "":
		t.Trash = TrashNone
	default:
		return Target{}, false
	}
	return t, true
}

// prefixFor rebuilds the request's own prefix: the mount as the request
// spelled it, plus the given number of segments after it.
//
// Taken from the escaped input rather than assembled from the parsed parts, so
// an href carries back exactly the encoding the client sent.
func prefixFor(escapedPath, mount string, segments int) string {
	p := collapseSlashes(escapedPath)
	front := ""
	if trimmed, ok := strings.CutPrefix(p, frontPrefix); ok {
		front = frontPrefix
		p = trimmed
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, mount), "/")
	kept := front + mount
	for range segments {
		if rest == "" {
			break
		}
		seg, tail, _ := strings.Cut(rest, "/")
		kept += "/" + seg
		rest = tail
	}
	return kept
}

// splitPath decodes a path into components, refusing anything that could name
// something other than a file below the tree.
//
// A component is decoded once. A decoded component holding a slash, a NUL, a
// dot-dot or nothing at all is refused rather than cleaned: cleaning turns a
// path the client did not write into one this server acts on.
func splitPath(escaped string) ([]string, error) {
	escaped = strings.Trim(escaped, "/")
	if escaped == "" {
		return nil, nil
	}
	parts := strings.Split(escaped, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, ErrBadPath
		}
		comp, err := url.PathUnescape(part)
		if err != nil {
			return nil, ErrBadPath
		}
		if comp == "" || comp == "." || comp == ".." ||
			strings.ContainsAny(comp, "/\x00") {
			return nil, ErrBadPath
		}
		out = append(out, comp)
	}
	return out, nil
}

// collapseSlashes folds repeated separators.
//
// A client that joins a base URL and a path can send two, and the same
// resource addressed two ways has to reach the same place: without this a
// listing and the file it names resolved differently and every second
// download failed.
func collapseSlashes(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p
}

// Vpath is the engine's own spelling of what this target addresses: the share
// label and the rest, joined.
func (t Target) Vpath() string { return strings.Join(t.Path, "/") }

// IsRoot reports whether the target names the tree itself rather than
// something inside it.
func (t Target) IsRoot() bool { return len(t.Path) == 0 }

// Href renders the address of a resource below this target's tree.
//
// Components are escaped one at a time so that a name holding a slash stays
// one component, and a collection carries the trailing slash a client uses to
// tell the two apart.
func (t Target) Href(comps []string, isDir bool) string {
	out := t.Prefix
	for _, c := range comps {
		out += "/" + escapeSegment(c)
	}
	if isDir {
		out += "/"
	}
	return out
}

// escapeSegment percent-encodes one path component.
//
// url.PathEscape leaves a handful of sub-delimiters alone, which is legal but
// not what the clients round-trip cleanly: one of them compares a decoded href
// against a path it encoded itself, so the encoding has to be the conservative
// one everywhere.
func escapeSegment(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			out = append(out, ch)
		case ch == '-', ch == '_', ch == '.', ch == '~':
			out = append(out, ch)
		default:
			const hex = "0123456789ABCDEF"
			out = append(out, '%', hex[ch>>4], hex[ch&0x0f])
		}
	}
	return string(out)
}

// ChunkName reads a chunk member's name as a number.
//
// The two clients spell it differently, zero-padded to six digits and plain,
// and both mean the same ordinal. A name that is not a number is not a chunk.
func ChunkName(member string) (uint32, bool) {
	if member == "" || len(member) > 10 {
		return 0, false
	}
	var n uint64
	for i := range len(member) {
		ch := member[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + uint64(ch-'0')
		if n > 1<<31 {
			return 0, false
		}
	}
	return uint32(n), true
}

// FormatChunkName is the spelling a listing reports a chunk under.
//
// Zero-padded to six digits, which is the form one client's resume logic
// recognises: it reads the collection, keeps the members whose names are
// numeric and at most six digits, and treats everything else as not a chunk.
func FormatChunkName(n uint32) string {
	return fmt.Sprintf("%06d", n)
}
