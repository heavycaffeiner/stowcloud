//go:build linux

package core

import (
	"context"
	"sort"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// List is the default listing: by name, ascending, one default-sized page.
func (c *Core) List(ctx context.Context, r Resolved, cur Cursor) (Page, error) {
	return c.ListSorted(ctx, r, cur, ListOptions{})
}

// ListSorted reads one directory, orders the whole of it, and cuts the page
// the cursor asks for.
//
// The order is applied across the directory before the cut, never within the
// page: sorting only the rows being returned gives an order that changes as
// somebody scrolls, since each page would be sorted against a different
// neighbourhood.
func (c *Core) ListSorted(ctx context.Context, r Resolved, cur Cursor, opt ListOptions) (Page, error) {
	if err := r.Require(acl.Read); err != nil {
		return Page{}, err
	}

	dirStat, err := r.root.Stat(r.path)
	if err != nil {
		return Page{}, mapVFSErr(err)
	}
	if !dirStat.Kind.IsDir() {
		// Not ErrDenied: listing a file is asking for something that is not
		// there, and a caller who may read the file learns nothing from
		// being refused instead of told.
		return Page{}, errf(ErrNotFound, "list a path that is not a directory")
	}

	offset, err := cursorOffset(cur)
	if err != nil {
		return Page{}, err
	}

	dir, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		return Page{}, mapVFSErr(err)
	}

	rows := make([]listRow, len(dir))
	for i, e := range dir {
		rows[i] = listRow{name: e.Name, kind: e.Kind, isFile: !e.Kind.IsDir()}
	}
	if opt.Sort.NeedsStat() {
		// One stat per name in the directory, not per name on the page: the
		// order is global, so every row has to be comparable before the cut.
		c.statAll(r, rows)
	}
	sortListing(rows, opt)

	dirs := 0
	for _, row := range rows {
		if !row.isFile {
			dirs++
		}
	}

	if offset > len(rows) {
		return Page{}, errf(ErrNotFound, "a listing cursor past the end of the directory")
	}
	size := pageSize
	if opt.Limit > 0 {
		size = min(opt.Limit, maxPageSize)
	}
	end := min(offset+size, len(rows))

	entries := make([]Entry, 0, end-offset)
	for _, row := range rows[offset:end] {
		p, jerr := r.path.JoinExisting(row.name)
		if jerr != nil {
			// A name the read showed and the join now refuses is a control
			// file that appeared between the two. Skipping the row is
			// cheaper than failing the directory over it.
			continue
		}
		e := c.buildEntryFromStat(r, row.name, p, row.st)
		if e.Kind == vfs.KindOther {
			// The stat could not type it, which is what a symlink under the
			// deny policy looks like. The directory read is the one source
			// that survives that, so it decides. The stat stays primary
			// because it resolves DT_UNKNOWN and the read does not.
			e.Kind = row.kind
			e.IsDir = row.kind.IsDir()
		}
		entries = append(entries, e)
	}

	etag, weak := FileETag(dirStat)
	page := Page{
		Entries:     entries,
		Dirs:        dirs,
		DirEtag:     etag,
		DirEtagWeak: weak,
		Total:       len(rows),
	}
	if end < page.Total {
		page.Next = Cursor(strconv.Itoa(end))
	}
	return page, nil
}

// listRow is one directory entry carried through the sort. The name, the
// kind and the sort keys travel as one value rather than as parallel slices
// indexed together, so a swap cannot move one and leave the others: an
// earlier revision of this sort permuted the names alone, and every kind
// stayed beside whichever name landed in its index.
type listRow struct {
	name   string
	kind   vfs.Kind
	isFile bool

	// size and mtime are filled only when the key needs them; an entry that
	// could not be stat'ed keeps the zero, which puts it at one end of its
	// group rather than dropping the row.
	size  uint64
	mtime int64
	st    *vfs.Stat
}

// statAll stats every row once, in place. A name that cannot be joined or
// stat'ed keeps its zero keys.
func (c *Core) statAll(r Resolved, rows []listRow) {
	for i := range rows {
		p, jerr := r.path.JoinExisting(rows[i].name)
		if jerr != nil {
			continue
		}
		st, serr := r.root.Stat(p)
		if serr != nil {
			continue
		}
		rows[i].size, rows[i].mtime = st.Size, st.MtimeNs
		copyStat := st
		rows[i].st = &copyStat
	}
}

// sortListing orders the whole directory. Directories are their own group
// ahead of files under every key, and Desc reverses within each group rather
// than putting files first, which is why the group test runs before the key
// and is never inverted.
//
// The sort is stable, so rows the key and the name both leave undecided keep
// the order the directory read returned.
func sortListing(rows []listRow, opt ListOptions) {
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].isFile != rows[b].isFile {
			return !rows[a].isFile
		}
		if less, decided := lessByKey(rows[a], rows[b], opt.Sort); decided {
			if opt.Desc {
				return !less
			}
			return less
		}
		// The name breaks every tie, ascending in both directions: two files
		// of one size keep a single relative order rather than depending on
		// what the kernel returned.
		return rows[a].name < rows[b].name
	})
}

// lessByKey compares two rows under one key, reporting decided == false when
// the key cannot tell them apart.
func lessByKey(a, b listRow, key SortKey) (less, decided bool) {
	switch key {
	case SortKind:
		if a.kind != b.kind {
			return a.kind < b.kind, true
		}
	case SortSize:
		if a.size != b.size {
			return a.size < b.size, true
		}
	case SortMtime:
		if a.mtime != b.mtime {
			return a.mtime < b.mtime, true
		}
	case SortName:
		if a.name != b.name {
			return a.name < b.name, true
		}
	}
	return false, false
}

// cursorOffset parses the wire form of a cursor: an ASCII decimal offset
// into the sorted listing, minted and read only here.
//
// The offset is a position in a listing taken per request, so a directory
// that changes between pages can skip or repeat an entry across the
// boundary. That is accepted: a cursor stable over a changing directory
// would need server-side listing state this design does not hold.
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

// buildEntry projects one name into the listing shape.
//
// A stat that fails means the entry vanished between the directory read and
// here, and the answer is a skeleton row rather than a failed listing: a
// directory that dies over one racing delete punishes everything else in it
// for the one row.
func (c *Core) buildEntry(r Resolved, name string, p vfs.SafePath) Entry {
	return c.buildEntryFromStat(r, name, p, nil)
}

func (c *Core) buildEntryFromStat(r Resolved, name string, p vfs.SafePath, st *vfs.Stat) Entry {
	if st == nil {
		s, err := r.root.Stat(p)
		if err != nil {
			return Entry{Name: name, Path: p.Share(), Perms: r.perms}
		}
		st = &s
	}
	etag, weak := FileETag(*st)
	return Entry{
		Name:     name,
		Path:     p.Share(),
		Kind:     st.Kind,
		IsDir:    st.Kind.IsDir(),
		Size:     st.Size,
		MTimeNs:  st.MtimeNs,
		BTimeNs:  st.BtimeNs,
		Ident:    ident.Of(r.share, *st),
		ETag:     etag,
		ETagWeak: weak,
		Perms:    r.perms,
	}
}
