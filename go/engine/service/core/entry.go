package core

import (
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Entry is the one listing shape. Every protocol renders from this and
// nothing adds a field for one protocol's benefit: a vendor-specific
// property is decorated at the protocol layer, never here.
type Entry struct {
	Name string
	Path vfs.SharePath

	// Kind is what the entry turned out to be, and IsDir beside it is
	// exactly Kind.IsDir(): a symlink is not a directory whatever it points
	// at, because under the default policy it cannot be entered.
	//
	// Both are carried because a client needs to tell a symlink from a file
	// to draw it and to decide what opening it means, and a boolean cannot.
	Kind  vfs.Kind
	IsDir bool

	Size    uint64
	MTimeNs int64

	// BTimeNs is a pointer because a filesystem without birth times has no
	// value to report, and zero is a real timestamp.
	BTimeNs *int64

	// Ident is the stable identity the store's ident package builds. Only
	// the core builds entries, so only the core mints identities, which is
	// what makes them trustworthy to a protocol.
	Ident ident.Ident

	ETag     string
	ETagWeak bool

	// Perms is the caller's effective permission set at this path. Under a
	// share link it is the link's permission set instead, overwritten by
	// the link surface.
	Perms acl.Perms
}

// Page holds a size-limited portion of a directory listing together with the
// counts a client requires to draw a grid.
type Page struct {
	Entries []Entry

	// Dirs is how many of Total were directories. They sort first, so this
	// is also the index where files begin, and a grid needs to know where
	// one run ends without having loaded the rows either side of the
	// boundary.
	Dirs int

	// DirEtag carries the directory's change token, letting a sync client
	// determine whether anything below changed without a second round trip.
	DirEtag     string
	DirEtagWeak bool

	// Next is empty on the final page.
	Next Cursor

	// Total is the whole directory, not just this page. Counted there for
	// the same reason as Dirs: the page is a slice that may hold neither
	// run's boundary.
	Total int
}

// Cursor marks an opaque position within a listing. An empty value requests the
// first page; any other value came from a previous Page.
//
// The content is an ASCII decimal offset, produced and consumed only here.
// Protocols treat it as opaque.
type Cursor string

// SortKey selects a listing's ordering. Under every key directories form their
// own group ahead of files, so descending order reverses within each group
// instead of promoting files to the top.
type SortKey uint8

const (
	// SortName is the default, and the only key that costs nothing: the
	// name and the kind both come back from the directory read itself.
	SortName SortKey = iota
	SortKind
	// SortSize and SortMtime need a stat per entry, because a directory
	// read returns neither. That is a syscall for every name in the
	// directory rather than for the page being returned.
	SortSize
	SortMtime
)

// ParseSortKey is the trust boundary for the query parameter. An unknown
// value is the default rather than a refusal: a listing is a read, and
// failing it over a spelling would take the folder away instead of showing
// it in an order the caller did not ask for.
func ParseSortKey(s string) SortKey {
	switch s {
	case "kind":
		return SortKind
	case "size":
		return SortSize
	case "mtime":
		return SortMtime
	default:
		return SortName
	}
}

// NeedsStat reports whether this ordering requires stating every entry rather
// than only those on the returned page.
func (k SortKey) NeedsStat() bool { return k == SortSize || k == SortMtime }

// ListOptions specifies a listing's ordering. The zero value means name
// ascending, which is what every indifferent caller receives.
type ListOptions struct {
	Sort SortKey

	// Desc inverts the ordering inside each group. Directories precede files
	// regardless.
	Desc bool

	// Limit is how many entries to return, and zero means pageSize. It
	// exists because the interface fetches the window it is about to draw,
	// which is not always one page.
	Limit int
}

// pageSize sets the entries per Page when the caller specifies no limit.
const pageSize = 200

// maxPageSize bounds what a caller may ask for. A window the interface
// scrolls is a few hundred rows; the ceiling is what stops one request from
// walking a whole directory into memory.
const maxPageSize = 2000
