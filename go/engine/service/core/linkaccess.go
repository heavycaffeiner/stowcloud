//go:build linux

package core

import (
	"context"
	"io"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// The complete set of operations a token holder may perform.
//
// Every surface here applies the same liveness rule, and the error mapping forms
// part of the security design. Unknown tokens yield ErrNotFound: a token naming
// nothing is absent rather than removed, and calling it removed would assert it
// once existed, letting a stranger separate guesses into tokens that were real
// links and tokens that never were. All other failures, including expiry, an
// exhausted cap, an unregistered share, an unparseable path, a stat failure and
// a failed identity cross-check, collapse into ErrLinkExpired: the link is dead
// and the response withholds the reason. Separating "renamed" from "cap
// exhausted" would disclose the target's history to anyone holding a stale
// token.
//
// A link's permissions cover its entire subtree. No per-entry ACL check occurs
// beneath a link, because a link constitutes one grant: the token holder gets
// exactly what the link was issued with and nothing further.

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

// linkTarget resolves a subpath under a link, or the link's own path when the
// subpath is empty.
//
// Subpaths are parsed rather than concatenated as text. ParseSafePath rejects
// "..", absolute paths and all reserved names, so a visitor cannot reference
// anything outside the folder the link was created for.
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
	return Resolved{user: link.Owner, share: link.Share, root: root, path: p, perms: perms}
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

// LinkCheckPassword tests a candidate against a link's stored hash.
//
// Links without a password accept any candidate. The verifier fails closed: an
// unconfigured one returns an error rather than succeeding. Nonexistent links
// never arrive here, since a bearer only ever holds a token that resolved.
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

// LinkEntry is a single row within a shared folder's listing.
type LinkEntry struct {
	Name  string
	IsDir bool
	Size  uint64
}

// LinkListing is the view a visitor gets at one path inside a shared folder.
type LinkListing struct {
	// Path gives the subpath relative to the link's root, and is empty at the
	// top level.
	Path string
	// IsDir says which of the two shapes this is. A file link answers false
	// with no entries, which is what lets one endpoint serve both.
	IsDir   bool
	Name    string
	Size    uint64
	Entries []LinkEntry
}

// LinkBrowse enumerates a shared folder at sub, or reports on a shared file.
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
		// Obtaining the size costs one stat per entry, justified for a listing
		// behind a public token: the page displays it, and reporting every file
		// as zero bytes would be incorrect rather than merely incomplete.
		// Entries whose stat fails retain the readdir kind and a zero size.
		if p, jerr := target.JoinExisting(e.Name); jerr == nil {
			if es, eerr := root.Stat(p); eerr == nil {
				row.Size = es.Size
				row.IsDir = es.Kind.IsDir()
			}
		}
		out.Entries = append(out.Entries, row)
	}
	// Directories lead, then name order, matching the browse screen.
	sort.SliceStable(out.Entries, func(i, j int) bool {
		if out.Entries[i].IsDir != out.Entries[j].IsDir {
			return out.Entries[i].IsDir
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}

// LinkResolved is the resolution granted by a link's own permissions at sub.
//
// It serves surfaces expecting a Resolved instead of a stream, and assigning it
// the link's permission set keeps the question of what a token may do in a
// single location.
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

// LinkArchiveWalk traverses a shared folder for the zip endpoint.
//
// It stands apart from ArchiveWalk because that function consults the ACL about
// what the resolved account may read, while a link has no account: the token is
// itself the grant. An ACL-driven walk beneath a link enters every directory and
// extracts nothing from them, which produced archives containing no files.
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

// linkWalkRec performs the descent, applying the link's grant to every entry.
func (c *Core) linkWalkRec(
	ctx context.Context, r Resolved, rel string, visit func(WalkEntry, *Stream) error,
) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		// Disappeared or became unreadable between the parent's check and this
		// step. Its contents are omitted rather than failing the entire
		// archive.
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
				if off+int64(n) > limits.RequestBody {
					return limits.Exceed("drop file", limits.RequestBody, off+int64(n))
				}
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
