//go:build linux

package core

import (
	"context"
	"io"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Browsing a link that points at a folder.
//
// A link names one path. Everything reachable through it is under that path
// and nowhere else: the subpath a visitor asks for is resolved beneath the
// link's own, by the same resolver every other read uses, so a link cannot be
// walked out of the folder it was made for.
//
// The link's permissions apply to the whole subtree. There is no per-entry
// check under a link, because a link is one grant: whoever holds the token has
// exactly what the link was given and nothing more.

// LinkEntry is one row of a shared folder's listing.
type LinkEntry struct {
	Name  string
	IsDir bool
	Size  uint64
}

// LinkListing is what a visitor sees at one path inside a shared folder.
type LinkListing struct {
	// Path is the subpath relative to the link's own root, empty at the top.
	Path string
	// IsDir says which of the two shapes this is. A file link answers with
	// IsDir false and no entries, which is what lets one endpoint serve both.
	IsDir   bool
	Name    string
	Size    uint64
	Entries []LinkEntry
}

// linkTarget resolves a subpath beneath a link, or the link's own path when
// the subpath is empty.
//
// The subpath is parsed rather than joined as text: ParseSafePath is what
// refuses "..", an absolute path and every reserved name, so a visitor cannot
// name anything outside the folder the link was made for.
func (c *Core) linkTarget(link Link, sub string) (*vfs.ShareRoot, vfs.SafePath, error) {
	root, ok := c.ShareRoot(link.Share)
	if !ok {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	base, perr := link.Path.Safe()
	if perr != nil {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	sub = strings.Trim(sub, "/")
	if sub == "" {
		return root, base, nil
	}
	rel, rerr := vfs.ParseSafePath(sub)
	if rerr != nil {
		return nil, vfs.SafePath{}, ErrNotFound
	}
	out := base
	for _, comp := range rel.Components() {
		next, jerr := out.JoinExisting(comp)
		if jerr != nil {
			return nil, vfs.SafePath{}, ErrNotFound
		}
		out = next
	}
	return root, out, nil
}

// LinkBrowse lists a shared folder at sub, or describes a shared file.
//
// The identity cross-check runs against the link's own root rather than the
// subpath: a rename of the shared folder makes the link gone, and a file
// inside it moving is an ordinary change to what the folder contains.
func (c *Core) LinkBrowse(ctx context.Context, link Link, sub string) (LinkListing, error) {
	now := c.clk.Nanos()
	if link.IsExpired(now) || link.IsExhausted() {
		return LinkListing{}, ErrLinkExpired
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return LinkListing{}, err
	}

	// The link's own path still has to be the thing it was made for.
	base, perr := link.Path.Safe()
	if perr != nil {
		return LinkListing{}, ErrLinkExpired
	}
	baseStat, berr := root.Stat(base)
	if berr != nil {
		return LinkListing{}, ErrLinkExpired
	}
	if link.Dev() != nil && !sameIdent(baseStat, link) {
		return LinkListing{}, ErrLinkExpired
	}

	st, serr := root.Stat(target)
	if serr != nil {
		return LinkListing{}, ErrNotFound
	}

	out := LinkListing{
		Path:  strings.Trim(sub, "/"),
		IsDir: st.Kind.IsDir(),
		Name:  target.Name(),
		Size:  st.Size,
	}
	if out.Name == "" {
		// The link's own root, whose name is the last component of the path it
		// was made for.
		out.Name = base.Name()
	}
	if !out.IsDir {
		return out, nil
	}

	all, rerr := root.ReadDir(target, vfs.HideReserved)
	if rerr != nil {
		return LinkListing{}, mapVFSErr(rerr)
	}
	out.Entries = make([]LinkEntry, 0, len(all))
	for _, e := range all {
		row := LinkEntry{Name: e.Name, IsDir: e.Kind.IsDir()}
		// The size costs a stat per entry, which a listing behind a public
		// token is worth: the page draws it, and a listing that showed every
		// file as zero bytes would be wrong rather than merely sparse.
		if p, jerr := target.JoinExisting(e.Name); jerr == nil {
			if es, eerr := root.Stat(p); eerr == nil {
				row.Size = es.Size
				row.IsDir = es.Kind.IsDir()
			}
		}
		out.Entries = append(out.Entries, row)
	}
	// Directories first, then by name, which is what the browse screen does.
	sort.SliceStable(out.Entries, func(i, j int) bool {
		if out.Entries[i].IsDir != out.Entries[j].IsDir {
			return out.Entries[i].IsDir
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}

// LinkArchiveWalk walks a shared folder for the zip endpoint.
//
// It is separate from ArchiveWalk because that one asks the ACL what the
// resolved user may read, and a link has no user: the token is the grant. A
// walk driven by the ACL under a link visits every directory and reads nothing
// out of them, which produced an archive with no files in it.
//
// Everything under the link is readable by definition, so there is no
// per-entry check here: the link's own permissions were checked once, by the
// caller, before the walk began.
func (c *Core) LinkArchiveWalk(ctx context.Context, link Link, sub string, visit func(WalkEntry, *Stream) error) error {
	now := c.clk.Nanos()
	if link.IsExpired(now) || link.IsExhausted() {
		return ErrLinkExpired
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return err
	}
	st, serr := root.Stat(target)
	if serr != nil {
		return ErrNotFound
	}
	r := Resolved{share: link.Share, root: root, path: target, perms: link.Perms}
	if !st.Kind.IsDir() {
		entry, stream, oerr := c.OpenStream(ctx, r, nil)
		if oerr != nil {
			return oerr
		}
		return visit(WalkEntry{
			RelPath: target.Name(), Readable: true,
			Size: entry.Size, MTimeNs: entry.MTime,
		}, stream)
	}
	return c.linkWalkRec(ctx, r, "", visit)
}

// linkWalkRec is the descent, with the link's grant standing for every entry.
func (c *Core) linkWalkRec(ctx context.Context, r Resolved, rel string, visit func(WalkEntry, *Stream) error) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		// Vanished or unreadable between the parent's check and this step.
		// Nothing further under it is reported rather than failing the archive.
		return nil
	}
	for _, e := range entries {
		childPath, jerr := r.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		childRel := e.Name
		if rel != "" {
			childRel = rel + "/" + e.Name
		}
		st, serr := r.root.Stat(childPath)
		if serr != nil {
			continue
		}
		child := Resolved{share: r.share, root: r.root, path: childPath, perms: r.perms}

		if st.Kind.IsDir() {
			if verr := visit(WalkEntry{RelPath: childRel, IsDir: true, Readable: true}, nil); verr != nil {
				return verr
			}
			if rerr := c.linkWalkRec(ctx, child, childRel, visit); rerr != nil {
				return rerr
			}
			continue
		}

		entry, stream, oerr := c.OpenStream(ctx, child, nil)
		if oerr != nil {
			if verr := visit(WalkEntry{RelPath: childRel, Readable: false}, nil); verr != nil {
				return verr
			}
			continue
		}
		verr := visit(WalkEntry{
			RelPath: childRel, Readable: true,
			Size: entry.Size, MTimeNs: entry.MTime,
		}, stream)
		_ = stream.Close() //nolint:errcheck // the visit's error is the answer.
		if verr != nil {
			return verr
		}
	}
	return nil
}

// LinkResolved is the resolution a link's own permissions grant at sub.
//
// It exists for the surfaces that take a Resolved rather than a stream: the
// archive walk is one, and giving it the link's permission set is what keeps
// "what may this token do" in one place.
func (c *Core) LinkResolved(link Link, sub string) (Resolved, error) {
	now := c.clk.Nanos()
	if link.IsExpired(now) || link.IsExhausted() {
		return Resolved{}, ErrLinkExpired
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{share: link.Share, root: root, path: target, perms: link.Perms}, nil
}

// LinkDropFile writes one uploaded file into a drop link's folder.
//
// The name is the visitor's, so it is parsed rather than joined as text and
// lands directly under the link's own path: a drop link admits files into one
// folder and not into a tree the uploader chooses.
//
// A name already taken is refused rather than replaced. Whoever holds a drop
// link cannot see what is in the folder, so allowing an overwrite would let
// them destroy a file they cannot name.
func (c *Core) LinkDropFile(ctx context.Context, link Link, name string, body io.Reader) (Entry, error) {
	now := c.clk.Nanos()
	if link.IsExpired(now) || link.IsExhausted() {
		return Entry{}, ErrLinkExpired
	}
	root, base, err := c.linkTarget(link, "")
	if err != nil {
		return Entry{}, err
	}
	if st, serr := root.Stat(base); serr != nil || !st.Kind.IsDir() {
		return Entry{}, ErrNotFound
	}
	target, jerr := base.Join(name)
	if jerr != nil {
		return Entry{}, errf(ErrNotFound, "a drop name this server refuses")
	}
	if _, serr := root.Stat(target); serr == nil {
		return Entry{}, ErrExists
	}

	// Write beside Create for this one call. Publishing a file is a write, and
	// the grant a drop link carries is "put a new file here": without it the
	// upload is refused by a check for an overwrite the no-clobber test above
	// has already ruled out.
	//
	// It does not widen the link. The permission is added to this resolution
	// and nowhere else, and every other surface still reads link.Perms.
	r := Resolved{share: link.Share, root: root, path: target, perms: link.Perms | acl.Write}
	// WriteAt rather than a copy: vfs.File is positional, which is what keeps
	// a write from depending on a shared offset.
	return c.CreateFile(ctx, r, vfs.DurableOpts{Mode: 0o664}, nil, func(f *vfs.File) error {
		var off int64
		buf := make([]byte, 1<<20)
		for {
			n, rerr := body.Read(buf)
			if n > 0 {
				if _, werr := f.WriteAt(buf[:n], off); werr != nil {
					return werr
				}
				off += int64(n)
			}
			if rerr == io.EOF {
				return f.Truncate(off)
			}
			if rerr != nil {
				return rerr
			}
		}
	})
}

// LinkStreamAt opens one file beneath a link, or the link's own file when sub
// is empty.
func (c *Core) LinkStreamAt(ctx context.Context, link Link, sub string, range_ *[2]uint64) (FidEntry, *Stream, error) {
	now := c.clk.Nanos()
	if link.IsExpired(now) || link.IsExhausted() {
		return FidEntry{}, nil, ErrLinkExpired
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return FidEntry{}, nil, err
	}
	base, perr := link.Path.Safe()
	if perr != nil {
		return FidEntry{}, nil, ErrLinkExpired
	}
	baseStat, berr := root.Stat(base)
	if berr != nil {
		return FidEntry{}, nil, ErrLinkExpired
	}
	if link.Dev() != nil && !sameIdent(baseStat, link) {
		return FidEntry{}, nil, ErrLinkExpired
	}
	st, serr := root.Stat(target)
	if serr != nil {
		return FidEntry{}, nil, ErrNotFound
	}
	if st.Kind.IsDir() {
		return FidEntry{}, nil, ErrNotFound
	}
	return c.OpenStream(ctx, Resolved{share: link.Share, root: root, path: target, perms: link.Perms}, range_)
}
