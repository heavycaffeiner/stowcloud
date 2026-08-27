//go:build linux

package core

import (
	"errors"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Resolved is what every operation takes instead of a virtual path: the
// share root, the validated path under it with the grant subpath already on
// the front, and the permissions the caller holds there.
//
// Every field is unexported, and that is the whole of the guarantee. A
// Resolved is a capability: no code outside this package can construct one,
// so the only way to hand an operation a target is to have passed the gate.
// This is also what forces the domain into a single package, since a
// sub-package could not read these fields without an exported constructor
// that would let arbitrary code skip the gate.
type Resolved struct {
	user  UserID
	share ShareID
	root  *vfs.ShareRoot
	path  vfs.SafePath
	perms acl.Perms
}

// User is the caller this resolution was made for.
func (r Resolved) User() UserID { return r.user }

// Share is the share this resolution landed in.
func (r Resolved) Share() ShareID { return r.share }

// Root is the live share root.
func (r Resolved) Root() *vfs.ShareRoot { return r.root }

// Path is the validated share-relative path, grant subpath on the front.
func (r Resolved) Path() vfs.SafePath { return r.path }

// Perms is the caller's full effective permission set at Path.
func (r Resolved) Perms() acl.Perms { return r.perms }

// Has reports whether the caller holds every bit in want here.
func (r Resolved) Has(want acl.Perms) bool { return r.perms.Has(want) }

// Require checks a permission the resolution's own need did not cover, so an
// operation demanding both read and download spends one gate rather than two
// resolutions.
func (r Resolved) Require(want acl.Perms) error {
	if r.perms.Has(want) {
		return nil
	}
	return ErrDenied
}

// Resolve turns a client path into a share root, a validated path under it,
// and the permissions the caller holds there. It is the single gate: nothing
// else in this package parses a virtual path, and no operation accepts one.
//
// A path outside every grant answers ErrNotFound, byte-identical to a path
// that is simply not on disk. Returning a denial there would tell a stranger
// the path exists, so the existence rule is applied in exactly one place and
// a denial can only be earned by a caller whose label already matched a
// grant.
//
// The path is not stat'ed here. Whether it exists is the consuming
// operation's problem, and its answer for a missing path is the same
// ErrNotFound, which is the point: at which layer the refusal happened is
// not observable.
func (c *Core) Resolve(user UserID, p vfs.Vpath, need acl.Perms) (Resolved, error) {
	if p.IsRoot() {
		// The virtual root names no share. It is listed through Roots and
		// never resolved.
		return Resolved{}, ErrNotFound
	}

	match, ok := c.rootFor(user, p.Label())
	if !ok {
		// Covers both a label the caller holds no grant over and a label
		// naming no share at all. To this caller they are one missing path.
		return Resolved{}, ErrNotFound
	}

	narrowed, err := num.Narrow[uint32](match.Share)
	if err != nil {
		return Resolved{}, errf(ErrNotFound, "the grant table holds a share id that does not fit")
	}
	share := ShareID(narrowed)
	entry, ok := c.shareEntry(share)
	if !ok {
		return Resolved{}, ErrNotFound
	}
	if entry.brokenErr != nil {
		// Deliberately not ErrNotFound. The caller holds a grant over this
		// share and sees it in their own root listing, so reporting the path
		// as missing would be the server contradicting that listing and
		// sending somebody whose drive did not come back looking for a
		// folder they think they deleted.
		return Resolved{}, &ShareBrokenError{
			Share:  entry.def.Name,
			Reason: RejectionKind(entry.brokenErr),
		}
	}

	full, err := c.joinSubpath(match.Subpath, p.Rest())
	if err != nil {
		return Resolved{}, err
	}

	at := acl.Vpath{Share: match.Share, Path: aclPath(full)}
	if !c.acl.Evaluate(int64(user), at, need).Allowed {
		return Resolved{}, ErrDenied
	}
	return Resolved{
		user:  user,
		share: share,
		root:  entry.root,
		path:  full,
		// The full effective set, so a later Require and an Entry's Perms
		// cost no second evaluation.
		perms: c.acl.Effective(int64(user), at),
	}, nil
}

// rootFor finds the caller's projected root carrying a label, first match
// winning. The projection already disambiguated duplicate labels with a
// " (2)" suffix, so the first match is the only match.
func (c *Core) rootFor(user UserID, label string) (acl.RootEntry, bool) {
	for _, r := range c.acl.Roots(int64(user)) {
		if r.Label == label {
			return r, true
		}
	}
	return acl.RootEntry{}, false
}

// joinSubpath lays the grant's subpath on the front of the caller's rest
// path.
//
// JoinExisting rather than Join: resolution addresses a path, it does not
// create one. The creation table refuses names an SMB client could never
// open, which is right for a name this server is about to mint and wrong for
// one another program already wrote; a directory literally named CON must
// stay listable and resolvable.
//
// A join failure on a grant subpath component is a corrupt grant, and it
// refuses the resolution rather than truncating the subpath: a grant that
// cannot name its own scope must not resolve to a wider one.
func (c *Core) joinSubpath(subpath acl.Path, rest vfs.SharePath) (vfs.SafePath, error) {
	full := vfs.RootPath()
	for _, comp := range subpath.Components() {
		next, err := full.JoinExisting(comp)
		if err != nil {
			return vfs.SafePath{}, err
		}
		full = next
	}
	if rest.IsRoot() {
		return full, nil
	}
	safe, err := rest.Safe()
	if err != nil {
		return vfs.SafePath{}, err
	}
	for _, comp := range safe.Components() {
		next, err := full.JoinExisting(comp)
		if err != nil {
			return vfs.SafePath{}, err
		}
		full = next
	}
	return full, nil
}

// aclPath is the one crossing from this package's validated path vocabulary
// into the evaluator's.
func aclPath(p vfs.SafePath) acl.Path { return acl.NewPath(p.Components()...) }

// ResolveUnder narrows a resolution onto a path beneath it, for the
// recursive walks that hold a Resolved for a directory and need one for a
// child they just listed. Re-resolving each child from a virtual path would
// re-run grant matching per entry, and it would also be wrong: once the
// grant subpath is on the front, the child's virtual path is not
// reconstructible from it.
//
// The permissions come from the parent rather than a fresh lookup, which is
// correct because a grant covers a subtree, and need is checked against them,
// so a walk can narrow but never widen. Under is component-wise, so a sibling
// whose name shares a byte prefix is not reachable.
func (c *Core) ResolveUnder(parent Resolved, p vfs.SafePath, need acl.Perms) (Resolved, error) {
	if !p.Under(parent.path) {
		return Resolved{}, errf(ErrDenied, "descend to a path that is not under the resolved one")
	}
	if !parent.perms.Has(need) {
		return Resolved{}, ErrDenied
	}
	return Resolved{
		user:  parent.user,
		share: parent.share,
		root:  parent.root,
		path:  p,
		perms: parent.perms,
	}, nil
}

// EntryAt is the projection of the resolved path itself, for a protocol that
// reports on a directory as well as its children. It lives in the core
// because an Entry carries an identity and a validator only the core mints,
// so building one outside the package is impossible by design.
func (c *Core) EntryAt(r Resolved, st vfs.Stat) Entry {
	etag, weak := FileETag(st)
	return Entry{
		Name:     r.path.Name(),
		Path:     r.path.Share(),
		Kind:     st.Kind,
		IsDir:    st.Kind.IsDir(),
		Size:     st.Size,
		MTimeNs:  st.MtimeNs,
		BTimeNs:  st.BtimeNs,
		Ident:    ident.Of(r.share, st),
		ETag:     etag,
		ETagWeak: weak,
		Perms:    r.perms,
	}
}

// VpathFor is the crossing back out: a share-relative path in the form this
// user's client sees it, under the label their own grant projects the share
// as. It answers "what is the URL of this" for search hits, recent listings
// and WebDAV hrefs.
//
// A share the user cannot see is an error rather than a guessed label,
// because a URL under a label they do not hold is a URL to nothing.
//
// Unlike Resolve, this does not strip the grant subpath: its callers pass
// paths in the same projected coordinate space the label roots.
func (c *Core) VpathFor(user UserID, share ShareID, p vfs.SharePath) (vfs.Vpath, error) {
	for _, r := range c.acl.Roots(int64(user)) {
		narrowed, err := num.Narrow[uint32](r.Share)
		if err != nil {
			continue
		}
		if ShareID(narrowed) == share {
			return vfs.NewVpath(r.Label, p)
		}
	}
	return vfs.Vpath{}, errors.New("vpath for an unreadable share")
}

// pathExists stats a path and folds the missing answer to false. It is the
// one way a mutation asks whether a destination is occupied without turning
// a refusal into an answer: a permission error stays an error, never a "no".
func pathExists(root *vfs.ShareRoot, p vfs.SafePath) (bool, error) {
	if _, err := root.Stat(p); err != nil {
		if errors.Is(err, vfs.ErrNotFound) {
			return false, nil
		}
		return false, mapVFSErr(err)
	}
	return true, nil
}

// requireCreatableLeaf applies the creation table to a leaf about to be
// brought into existence, by re-joining it onto its parent and discarding the
// result. The root passes, since nothing is being created there by name.
//
// This is the asymmetry partner of joinSubpath: anything already on the share
// stays fully usable, and nothing typed through this server adds a name a
// Windows or SMB client could never open.
func requireCreatableLeaf(p vfs.SafePath) error {
	if p.IsRoot() {
		return nil
	}
	_, err := p.Parent().Join(p.Name())
	return err
}

// uniqueNameBound is where the search below gives up. A directory holding
// this many collisions of one name is one where the caller wanted a different
// answer than a longer suffix, and an unbounded loop over a syscall per
// candidate is a request that never returns.
const uniqueNameBound = 10_000

// uniqueSiblingName picks the next free "stem (n).ext" beside a taken path, n
// counting from 2. It is what "keep both" resolves to and what a drop link
// does with a colliding upload, so the suffix a person sees is the same
// wherever this server had to invent a name.
func (c *Core) uniqueSiblingName(root *vfs.ShareRoot, taken vfs.SafePath) (vfs.SafePath, error) {
	return c.uniqueSiblingNameWithin(root, taken, uniqueNameBound)
}

func (c *Core) uniqueSiblingNameWithin(root *vfs.ShareRoot, taken vfs.SafePath, bound int) (vfs.SafePath, error) {
	dir := taken.Parent()
	name := taken.Name()
	stem, ext := name, ""
	// i > 0 rather than i >= 0: a leading dot is a hidden file's name, not an
	// extension, so ".bashrc" becomes ".bashrc (2)" and never " (2).bashrc".
	if i := lastDot(name); i > 0 {
		stem, ext = name[:i], name[i:]
	}
	for n := 2; n < bound; n++ {
		// Join, so an invented name still passes the creation table. One the
		// table refuses is skipped rather than fatal.
		candidate, err := dir.Join(stem + " (" + strconv.Itoa(n) + ")" + ext)
		if err != nil {
			continue
		}
		exists, err := pathExists(root, candidate)
		if err != nil {
			return vfs.SafePath{}, err
		}
		if !exists {
			return candidate, nil
		}
	}
	return vfs.SafePath{}, ErrConflict
}

// lastDot is the index of the last '.' in a name, or -1 when there is none.
func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
