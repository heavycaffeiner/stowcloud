//go:build linux

package core

import (
	"context"
	"errors"
	"sort"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Entry is the one listing shape. Every protocol renders from this and nothing
// adds a field for one protocol's benefit: a vendor-specific property is
// decorated at the protocol layer through its PropSource hook, never here.
type Entry struct {
	Name string
	Path vfs.SharePath
	// Kind is what the entry turned out to be. IsDir beside it is "== dir"
	// and nothing more: a symlink is not a directory whatever it points at,
	// because under the default policy it cannot be entered.
	//
	// Both are carried because a client needs to tell a symlink from a file to
	// draw it and to decide what opening it means, and a boolean cannot: every
	// symlink reached the interface as an ordinary file.
	Kind     vfs.Kind
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

// SortKey is what a listing is ordered by. Directories are their own group
// ahead of files under every one of them, so a descending order reverses
// within each group rather than putting files first.
type SortKey uint8

const (
	// SortName is the default, and the only key that costs nothing: the name
	// and the kind both come back from the directory read itself.
	SortName SortKey = iota
	SortKind
	// SortSize and SortMtime need a stat per entry, because a directory read
	// returns neither. That is a syscall for every name in the directory
	// rather than for the page being returned, which is what ordering the
	// whole listing by a value only a stat knows actually costs.
	SortSize
	SortMtime
)

// ParseSortKey is the trust boundary for the query parameter. An unknown value
// is the default rather than a refusal: a listing is a read, and failing it
// over a spelling would take the folder away instead of showing it in an order
// the caller did not ask for.
func ParseSortKey(s string) SortKey {
	switch s {
	case "kind":
		return SortKind
	case "size":
		return SortSize
	case "mtime":
		return SortMtime
	}
	return SortName
}

// NeedsStat reports whether ordering by this key has to stat every entry
// rather than only the page being returned.
func (k SortKey) NeedsStat() bool { return k == SortSize || k == SortMtime }

// ListOptions is how a listing is ordered. The zero value is the default:
// by name, ascending, which is what every caller that does not care gets.
type ListOptions struct {
	Sort SortKey
	// Desc reverses the order within each group. Directories stay ahead of
	// files either way.
	Desc bool
	// Limit is how many entries to return. Zero means the default page size.
	// It exists because the interface fetches the window it is about to draw,
	// which is not always one page.
	Limit int
}

// pageSize is how many entries one Page holds when the caller names no bound
// of its own.
const pageSize = 200

// maxPageSize bounds what a caller may ask for. A window the interface scrolls
// is a few hundred rows; the ceiling is what stops one request from walking a
// whole directory into memory.
const maxPageSize = 2000

// List returns one page of a directory.
//
// Directories sort as their own group ahead of files under every key, so a
// descending order flips within each group and never puts files first.
func (c *Core) List(ctx context.Context, r Resolved, cur Cursor) (Page, error) {
	return c.ListSorted(ctx, r, cur, ListOptions{})
}

// ListSorted is List with the order the caller asked for.
//
// The order is applied across the whole directory before the page is cut, not
// within the page: sorting a slice of an unsorted listing is an order that
// changes as somebody scrolls.
//
// opt.Limit is how many entries the caller wants back. Zero is the default
// page, and anything past maxPageSize is clamped rather than refused: a client
// asking for more than the ceiling wants as much as it can have.
func (c *Core) ListSorted(ctx context.Context, r Resolved, cur Cursor, opt ListOptions) (Page, error) {
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
	// Ordering by size or mtime needs a value the directory read does not
	// carry, so those keys stat every entry before the sort. The default key
	// costs nothing, which is what keeps a large directory listing at one
	// syscall per page rather than one per file.
	var stats map[string]vfs.Stat
	if opt.Sort.NeedsStat() {
		stats = c.statAll(r, names)
	}
	sortListing(all, names, opt, stats)

	// Counted over the whole directory rather than the page: it is what the
	// grid draws the boundary between the two runs from, and the page it was
	// counted over is a slice that may hold neither.
	totalDirs := 0
	for _, e := range all {
		if e.Kind.IsDir() {
			totalDirs++
		}
	}

	if offset > len(names) {
		return Page{}, errf(ErrNotFound, "a listing cursor past the end of the directory")
	}

	size := pageSize
	if opt.Limit > 0 {
		size = min(opt.Limit, maxPageSize)
	}
	end := min(offset+size, len(names))
	entries := make([]Entry, 0, end-offset)
	for i, name := range names[offset:end] {
		p, jerr := r.path.JoinExisting(name)
		if jerr != nil {
			// A name the listing already showed must be joinable; a refusal
			// here is a control-file race, and skipping the row is safer than
			// failing the whole directory over one.
			continue
		}
		e := c.buildEntry(r, name, p)
		// The directory read already knows what each name is, and it is the
		// only source that survives a stat this resolver will not do: a
		// symlink cannot be opened under the default policy, so its stat
		// fails and the entry would otherwise be typed "other". Kept as the
		// fallback rather than the primary, because the stat resolves
		// DT_UNKNOWN and the directory read does not.
		if e.Kind == vfs.KindOther {
			e.Kind = all[offset+i].Kind
		}
		entries = append(entries, e)
	}

	etag, weak := FileETag(dirStat)
	page := Page{
		Entries:     entries,
		DirEtag:     etag,
		DirEtagWeak: weak,
		Total:       len(names),
		Dirs:        totalDirs,
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
		Kind:     st.Kind,
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

// statAll stats every name once, for the two keys that order by a value the
// directory read does not carry. A name that cannot be stated is left out and
// sorts as a zero, which puts it at one end rather than dropping the row.
func (c *Core) statAll(r Resolved, names []string) map[string]vfs.Stat {
	out := make(map[string]vfs.Stat, len(names))
	for _, name := range names {
		p, jerr := r.path.JoinExisting(name)
		if jerr != nil {
			continue
		}
		st, serr := r.root.Stat(p)
		if serr != nil {
			continue
		}
		out[name] = st
	}
	return out
}

// sortListing orders the whole directory by the requested key.
//
// Directories are their own group ahead of files under every key, so a
// descending order reverses within each group and never puts files first. That
// is what a file manager does, and it is why the reverse is not a plain
// negation of the comparison.
func sortListing(entries []vfs.DirEntry, names []string, opt ListOptions, stats map[string]vfs.Stat) {
	isFile := make([]bool, len(entries))
	for i, e := range entries {
		isFile[i] = !e.Kind.IsDir()
	}

	// All three move together. Swapping names alone left every kind beside
	// whichever name landed in its index, so a directory read that did not
	// arrive already sorted produced folders drawn as files and files drawn as
	// folders.
	swap := func(a, b int) {
		names[a], names[b] = names[b], names[a]
		isFile[a], isFile[b] = isFile[b], isFile[a]
		entries[a], entries[b] = entries[b], entries[a]
	}

	sort.Stable(sortAdapter{n: len(names), swap: swap, less: func(a, b int) bool {
		if isFile[a] != isFile[b] {
			return !isFile[a] // directories first, in both orders
		}
		if less, decided := lessByKey(entries, names, stats, opt.Sort, a, b); decided {
			if opt.Desc {
				return !less
			}
			return less
		}
		// Equal under the chosen key: the name breaks the tie, and it does so
		// in the ascending direction either way, so two files of the same size
		// keep a stable order rather than depending on what the kernel
		// returned.
		return names[a] < names[b]
	}})
}

// lessByKey compares two entries under one key. decided is false when the key
// cannot tell them apart, which is what hands the comparison to the name.
func lessByKey(entries []vfs.DirEntry, names []string, stats map[string]vfs.Stat, key SortKey, a, b int) (less, decided bool) {
	switch key {
	case SortKind:
		if entries[a].Kind != entries[b].Kind {
			return entries[a].Kind < entries[b].Kind, true
		}
	case SortSize:
		sa, sb := stats[names[a]].Size, stats[names[b]].Size
		if sa != sb {
			return sa < sb, true
		}
	case SortMtime:
		ma, mb := stats[names[a]].MtimeNs, stats[names[b]].MtimeNs
		if ma != mb {
			return ma < mb, true
		}
	case SortName:
		if names[a] != names[b] {
			return names[a] < names[b], true
		}
	}
	return false, false
}

// sortAdapter drives sort.SliceStable over three parallel slices, which is
// what keeps the name, the kind and the entry together through every swap.
type sortAdapter struct {
	n    int
	less func(a, b int) bool
	swap func(a, b int)
}

func (s sortAdapter) Len() int           { return s.n }
func (s sortAdapter) Less(a, b int) bool { return s.less(a, b) }
func (s sortAdapter) Swap(a, b int)      { s.swap(a, b) }

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
