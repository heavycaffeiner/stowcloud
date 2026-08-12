//go:build linux

package core

import (
	"context"
	"errors"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Entry is the one listing shape. Every protocol renders from this and nothing
// adds a field for one protocol's benefit: a vendor-specific property is
// decorated at the protocol layer through its PropSource hook, never here.
type Entry struct {
	Name     string
	Path     vfs.SharePath
	IsDir    bool
	Size     uint64
	MTimeNs  int64
	BTimeNs  *int64
	Ident    cache.Ident
	ETag     string
	ETagWeak bool
	Perms    acl.Perms
}

// Page is one bounded slice of a directory listing, plus the accounting a
// client needs to render a grid: how many entries there were in total, how
// many were directories, the directory's own change token, and where the next
// page begins.
type Page struct {
	Entries []Entry
	// Dirs is how many of Total were directories. They sort first, so this is
	// also the index where files begin, and a grid needs to know where one run
	// ends without having loaded the rows either side of the boundary.
	Dirs int
	// DirEtag is the directory's own change token, so a sync client can ask
	// whether anything under here changed without a second round trip.
	DirEtag     string
	DirEtagWeak bool
	// Next is empty when this page is the last.
	Next Cursor
	// Total is the whole directory, not just this page.
	Total int
}

// Cursor is an opaque position in a listing. The empty value is the first
// page; anything else is a value a previous Page returned.
type Cursor string

// pageSize is how many entries one Page holds. The proposal's example uses
// two hundred and there is no way to override it through the contracted List
// signature, so it is a fixed constant here.
const pageSize = 200

// List returns one page of a directory.
//
// Directories sort as their own group ahead of files under every key, so a
// descending order flips within each group and never puts files first.
func (c *Core) List(ctx context.Context, r Resolved, cur Cursor) (Page, error) {
	if err := r.Require(acl.Read); err != nil {
		return Page{}, err
	}

	dirStat, err := r.root.Stat(r.path)
	if err != nil {
		return Page{}, mapVFSErr(err)
	}
	if !dirStat.Kind.IsDir() {
		return Page{}, errf(ErrNotFound, "list a path that is not a directory")
	}

	offset, err := cursorOffset(cur)
	if err != nil {
		return Page{}, err
	}

	all, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		return Page{}, mapVFSErr(err)
	}
	names := make([]string, 0, len(all))
	for _, e := range all {
		names = append(names, e.Name)
	}
	sortDirsFirst(all, names)

	if offset > len(names) {
		return Page{}, errf(ErrNotFound, "a listing cursor past the end of the directory")
	}

	end := min(offset+pageSize, len(names))
	entries := make([]Entry, 0, end-offset)
	for _, name := range names[offset:end] {
		p, jerr := r.path.JoinExisting(name)
		if jerr != nil {
			// A name the listing already showed must be joinable; a refusal
			// here is a control-file race, and skipping the row is safer than
			// failing the whole directory over one.
			continue
		}
		entries = append(entries, c.buildEntry(r, name, p))
	}

	etag, weak := FileETag(dirStat)
	page := Page{
		Entries:     entries,
		DirEtag:     etag,
		DirEtagWeak: weak,
		Total:       len(names),
	}
	for _, e := range entries {
		if e.IsDir {
			page.Dirs++
		}
	}
	if end < page.Total {
		page.Next = Cursor(strconv.Itoa(end))
	}
	return page, nil
}

// cursorOffset parses a listing cursor. The empty cursor is the first page;
// anything else is an ASCII decimal offset, which is the only value this
// package ever minted.
func cursorOffset(cur Cursor) (int, error) {
	if cur == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(string(cur))
	if err != nil || n < 0 {
		return 0, errf(ErrNotFound, "a malformed listing cursor")
	}
	return n, nil
}

// buildEntry is the projection of one directory entry.
func (c *Core) buildEntry(r Resolved, name string, p vfs.SafePath) Entry {
	st, err := r.root.Stat(p)
	if err != nil {
		// The entry vanished between the read and the stat. Report the
		// skeleton rather than failing the whole directory over one delete
		// race.
		return Entry{Name: name, Path: p.Share(), Perms: r.perms}
	}
	etag, weak := FileETag(st)
	return Entry{
		Name:     name,
		Path:     p.Share(),
		IsDir:    st.Kind.IsDir(),
		Size:     st.Size,
		MTimeNs:  st.MtimeNs,
		BTimeNs:  st.BtimeNs,
		Ident:    cache.IdentOf(r.share, st),
		ETag:     etag,
		ETagWeak: weak,
		Perms:    r.perms,
	}
}

// sortDirsFirst orders the directory: directories ahead of files, each group
// by name. names is the parallel name sequence being sorted.
func sortDirsFirst(entries []vfs.DirEntry, names []string) {
	isFile := make([]bool, len(entries))
	for i, e := range entries {
		isFile[i] = !e.Kind.IsDir()
	}
	// Insertion sort: the set is the size of one directory read, which is
	// bounded, and the names are unique so no stability is needed.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && lessDirsFirst(names, isFile, j-1, j); j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
}

// lessDirsFirst reports whether a should come before b.
func lessDirsFirst(names []string, isFile []bool, a, b int) bool {
	if isFile[a] != isFile[b] {
		return !isFile[a] // a is a directory and b is not
	}
	return names[a] < names[b]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// mapVFSErr converts a VFS error into a core sentinel. The existence rule is
// applied in Resolve, so a VFS NotFound here is a real missing path and maps
// to the same ErrNotFound the resolver returns.
func mapVFSErr(err error) error {
	switch {
	case errors.Is(err, vfs.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, vfs.ErrDenied), errors.Is(err, vfs.ErrSymlinkDenied):
		return ErrDenied
	case errors.Is(err, vfs.ErrExists):
		return ErrExists
	case errors.Is(err, vfs.ErrNotEmpty):
		return ErrNotEmpty
	case errors.Is(err, vfs.ErrNoSpace):
		return ErrNoSpace
	case errors.Is(err, vfs.ErrCrossDevice):
		return ErrCrossShare
	default:
		return err
	}
}
