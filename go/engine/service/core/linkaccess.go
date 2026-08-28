//go:build linux

package core

import (
	"context"
	"io"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// Everything a bearer of a token can do.
//
// The liveness rule is the same on every surface here, and the error mapping
// is part of the security design. An unknown token is ErrNotFound: a token
// that names nothing is absent, not gone, and reporting it as gone would
// assert it once existed, letting a stranger sort guesses into tokens that
// were real links and tokens that never were. Every other failure, expiry, an
// exhausted cap, an unregistered share, an unparseable path, a stat failure
// and a failed identity cross-check, collapses to ErrLinkExpired: the link is
// dead and the answer does not say why. Distinguishing "renamed" from "cap
// ran out" leaks the target's history to whoever holds a stale token.
//
// The link's permissions apply to the whole subtree. There is no per-entry
// ACL check under a link, because a link is one grant: whoever holds the
// token has exactly what the link was given and nothing more.

// linkLive is the expiry and cap half of the rule, which every surface runs
// before it touches the filesystem.
func (c *Core) linkLive(link Link) error {
	if link.IsExpired(c.clk.Nanos()) || link.IsExhausted() {
		return ErrLinkExpired
	}
	return nil
}

// linkBase resolves the link's own path and cross-checks the pinned identity.
//
// The check runs against the link's own root rather than a subpath: a rename
// of the shared folder kills the link, while a file moving inside the folder
// is an ordinary change to what the folder contains.
func (c *Core) linkBase(link Link) (*vfs.ShareRoot, vfs.SafePath, error) {
	root, ok := c.ShareRoot(link.Share)
	if !ok {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	base, perr := link.Path.Safe()
	if perr != nil {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	st, serr := root.Stat(base)
	if serr != nil {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	if link.Dev() != nil && !sameIdent(st, link) {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	return root, base, nil
}

// linkTarget resolves a subpath beneath a link, or the link's own path when
// the subpath is empty.
//
// The subpath is parsed rather than joined as text: ParseSafePath refuses
// "..", an absolute path and every reserved name, so a visitor cannot name
// anything outside the folder the link was made for.
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

// linkResolvedAt mints the resolution a link's permissions grant at a path.
// It is the one place a bearer's capability is turned into a Resolved.
func linkResolvedAt(link Link, root *vfs.ShareRoot, p vfs.SafePath, perms acl.Perms) Resolved {
	return Resolved{share: link.Share, root: root, path: p, perms: perms}
}

// LinkPublic resolves a token for a bearer, enforcing every liveness rule.
func (c *Core) LinkPublic(ctx context.Context, token string) (Link, Entry, error) {
	link, ok, err := c.resolveLink(ctx, token)
	if err != nil {
		return Link{}, Entry{}, err
	}
	if !ok {
		return Link{}, Entry{}, ErrNotFound
	}
	if lerr := c.linkLive(link); lerr != nil {
		return Link{}, Entry{}, lerr
	}
	root, base, err := c.linkBase(link)
	if err != nil {
		return Link{}, Entry{}, err
	}

	r := linkResolvedAt(link, root, base, link.Perms)
	entry := c.buildEntry(r, base.Name(), base)
	entry.Perms = link.Perms
	return link, entry, nil
}

// LinkStream opens a link's own target for ranged reading. It exists for the
// public download path, which has no user session to resolve through.
func (c *Core) LinkStream(ctx context.Context, link Link, range_ *[2]uint64) (FidEntry, *Stream, error) {
	if err := c.linkLive(link); err != nil {
		return FidEntry{}, nil, err
	}
	root, base, err := c.linkBase(link)
	if err != nil {
		return FidEntry{}, nil, err
	}
	return c.OpenStream(ctx, linkResolvedAt(link, root, base, link.Perms), range_)
}

// LinkStreamAt opens one file beneath a folder link, or the link's own file
// when sub is empty.
//
// A missing or directory subpath is ErrNotFound rather than a dead link: the
// subpath layer is a listing namespace, and a missing entry inside a live
// link is an ordinary miss.
func (c *Core) LinkStreamAt(
	ctx context.Context, link Link, sub string, range_ *[2]uint64,
) (FidEntry, *Stream, error) {
	if err := c.linkLive(link); err != nil {
		return FidEntry{}, nil, err
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return FidEntry{}, nil, err
	}
	if _, _, berr := c.linkBase(link); berr != nil {
		return FidEntry{}, nil, berr
	}
	st, serr := root.Stat(target)
	if serr != nil || st.Kind.IsDir() {
		return FidEntry{}, nil, ErrNotFound
	}
	return c.OpenStream(ctx, linkResolvedAt(link, root, target, link.Perms), range_)
}

// LinkCheckPassword verifies a candidate against a link's stored hash.
//
// A link with no password accepts anything. The verifier fails closed: an
// unwired one errors rather than passing. A nonexistent link cannot reach
// here, because a bearer only ever carries a token that resolved.
func (c *Core) LinkCheckPassword(ctx context.Context, link Link, candidate string) (bool, error) {
	store, err := c.links()
	if err != nil {
		return false, err
	}
	hash, err := store.PasswordHash(ctx, link.ID)
	if err != nil {
		return false, err
	}
	if hash == nil {
		return true, nil
	}
	return c.verifyLinkPassword(ctx, *hash, candidate)
}

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
	// IsDir says which of the two shapes this is. A file link answers false
	// with no entries, which is what lets one endpoint serve both.
	IsDir   bool
	Name    string
	Size    uint64
	Entries []LinkEntry
}

// LinkBrowse lists a shared folder at sub, or describes a shared file.
func (c *Core) LinkBrowse(ctx context.Context, link Link, sub string) (LinkListing, error) {
	if err := c.linkLive(link); err != nil {
		return LinkListing{}, err
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return LinkListing{}, err
	}
	_, base, err := c.linkBase(link)
	if err != nil {
		return LinkListing{}, err
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
		// The link's own root, whose name is the last component of the path
		// it was made for.
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
		// token is worth: the page draws it, and a listing showing every file
		// as zero bytes would be wrong rather than merely sparse. An entry
		// whose stat fails keeps the readdir kind and a zero size.
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

// LinkResolved is the resolution a link's own permissions grant at sub.
//
// It exists for the surfaces that take a Resolved rather than a stream, and
// giving it the link's permission set keeps "what may this token do" in one
// place.
func (c *Core) LinkResolved(link Link, sub string) (Resolved, error) {
	if err := c.linkLive(link); err != nil {
		return Resolved{}, err
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return Resolved{}, err
	}
	return linkResolvedAt(link, root, target, link.Perms), nil
}

// LinkArchiveWalk walks a shared folder for the zip endpoint.
//
// It is separate from ArchiveWalk because that one asks the ACL what the
// resolved user may read, and a link has no user: the token is the grant. A
// walk driven by the ACL under a link visits every directory and reads
// nothing out of them, which produced an archive with no files in it.
func (c *Core) LinkArchiveWalk(
	ctx context.Context, link Link, sub string, visit func(WalkEntry, *Stream) error,
) error {
	if err := c.linkLive(link); err != nil {
		return err
	}
	root, target, err := c.linkTarget(link, sub)
	if err != nil {
		return err
	}
	st, serr := root.Stat(target)
	if serr != nil {
		return ErrNotFound
	}

	r := linkResolvedAt(link, root, target, link.Perms)
	if !st.Kind.IsDir() {
		entry, stream, oerr := c.OpenStream(ctx, r, nil)
		if oerr != nil {
			return oerr
		}
		verr := visit(WalkEntry{
			RelPath: target.Name(), Readable: true,
			Size: entry.Size, MTimeNs: entry.MTime,
		}, stream)
		return firstErr(verr, stream.Close())
	}
	return c.linkWalkRec(ctx, r, "", visit)
}

// linkWalkRec is the descent, with the link's grant standing for every entry.
func (c *Core) linkWalkRec(
	ctx context.Context, r Resolved, rel string, visit func(WalkEntry, *Stream) error,
) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		// Vanished or unreadable between the parent's check and this step.
		// Nothing under it is reported rather than failing the whole archive.
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
			// A file that will not open is visited as unreadable rather than
			// failing the archive around it.
			if verr := visit(WalkEntry{RelPath: childRel, Readable: false}, nil); verr != nil {
				return verr
			}
			continue
		}
		verr := visit(WalkEntry{
			RelPath: childRel, Readable: true,
			Size: entry.Size, MTimeNs: entry.MTime,
		}, stream)
		if cerr := firstErr(verr, stream.Close()); cerr != nil {
			return cerr
		}
	}
	return nil
}

// linkDropDir is the common preamble of both drop surfaces: a live link
// carrying Create, whose own path is a directory.
func (c *Core) linkDropDir(link Link) (*vfs.ShareRoot, vfs.SafePath, error) {
	if !link.Perms.Has(acl.Create) {
		return nil, vfs.SafePath{}, ErrDenied
	}
	if err := c.linkLive(link); err != nil {
		return nil, vfs.SafePath{}, err
	}
	root, ok := c.ShareRoot(link.Share)
	if !ok {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	base, perr := link.Path.Safe()
	if perr != nil {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	st, serr := root.Stat(base)
	if serr != nil || !st.Kind.IsDir() {
		return nil, vfs.SafePath{}, ErrLinkExpired
	}
	return root, base, nil
}

// LinkDrop accepts a buffered upload through a drop link.
//
// A name already taken gets the counting suffix, the same "name (2).ext"
// shape the keep-both conflict policy uses, so one server invents one shape
// of name. It never overwrites: the bearer cannot see the folder, so an
// overwrite would let them destroy, or probe for, a file they cannot name.
func (c *Core) LinkDrop(ctx context.Context, link Link, name string, body []byte) (Entry, error) {
	root, base, err := c.linkDropDir(link)
	if err != nil {
		return Entry{}, err
	}
	// The name is the visitor's, so it goes through the safe join and lands
	// directly under the link's own path.
	dest, jerr := base.Join(name)
	if jerr != nil {
		return Entry{}, jerr
	}
	taken, err := pathExists(root, dest)
	if err != nil {
		return Entry{}, err
	}
	if taken {
		uniq, uerr := c.uniqueSiblingName(root, dest)
		if uerr != nil {
			return Entry{}, uerr
		}
		dest = uniq
	}

	// NoClobber, so the no-overwrite decision is enforced by the filesystem
	// open rather than only by the check above it: a race with a concurrent
	// upload cannot clobber.
	opts := vfs.DurableOpts{Mode: root.Policy().ModeFile, NoClobber: true}
	if _, werr := root.WriteDurable(dest, opts, func(f *vfs.File) error {
		_, cerr := f.WriteAt(body, 0)
		return cerr
	}); werr != nil {
		return Entry{}, mapVFSErr(werr)
	}

	c.markDirty(ctx, link.Share, dest)
	r := linkResolvedAt(link, root, dest, link.Perms)
	entry := c.buildEntry(r, dest.Name(), dest)
	entry.Perms = link.Perms
	return entry, nil
}

// LinkDropFile writes one streamed upload into a drop link's folder.
//
// A taken name is ErrExists rather than the counting suffix: this serves a
// streaming endpoint that reports the conflict, where LinkDrop serves a form
// post that retries with a new name. Both policies are kept.
func (c *Core) LinkDropFile(ctx context.Context, link Link, name string, body io.Reader) (Entry, error) {
	root, base, err := c.linkDropDir(link)
	if err != nil {
		return Entry{}, err
	}
	target, jerr := base.Join(name)
	if jerr != nil {
		return Entry{}, errf(ErrNotFound, "a drop name this server refuses")
	}
	taken, err := pathExists(root, target)
	if err != nil {
		return Entry{}, err
	}
	if taken {
		return Entry{}, ErrExists
	}

	// Write beside Create for this one resolution only. Publishing a file is
	// a write, and the no-clobber check above has already ruled out the
	// overwrite that Write would otherwise permit. Every other surface still
	// reads link.Perms, so the link itself is not widened.
	r := linkResolvedAt(link, root, target, link.Perms|acl.Write)
	opts := vfs.DurableOpts{Mode: root.Policy().ModeFile, NoClobber: true}
	entry, err := c.CreateFile(ctx, r, opts, nil, func(f *vfs.File) error {
		// WriteAt rather than a copy: vfs.File is positional, which keeps a
		// write from depending on a shared offset.
		var off int64
		buf := make([]byte, dropCopyBufBytes)
		for {
			n, rerr := body.Read(buf)
			if n > 0 {
				if _, werr := f.WriteAt(buf[:n], off); werr != nil {
					return werr
				}
				off += int64(n)
			}
			if rerr == io.EOF {
				// Truncate to what was actually written, so a reused staging
				// file cannot leave a tail behind.
				return f.Truncate(off)
			}
			if rerr != nil {
				return rerr
			}
		}
	})
	if err != nil {
		return Entry{}, err
	}
	entry.Perms = link.Perms
	return entry, nil
}

// dropCopyBufBytes is the streaming copy's buffer.
const dropCopyBufBytes = 1 << 20
