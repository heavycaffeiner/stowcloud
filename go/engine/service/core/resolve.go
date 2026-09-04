//go:build linux

package core

import (
	"context"
	"errors"
	"strconv"
	"strings"

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

// User names the caller this resolution serves.
func (r Resolved) User() UserID { return r.user }

// Share identifies where this resolution landed.
func (r Resolved) Share() ShareID { return r.share }

// Root is the live share root.
func (r Resolved) Root() *vfs.ShareRoot { return r.root }

// Path holds the validated share-relative path, prefixed by the grant subpath.
func (r Resolved) Path() vfs.SafePath { return r.path }

// Perms holds the caller's complete effective permissions at Path.
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

// Resolve converts a client path into a share root, a validated path beneath it,
// and the permissions the caller holds there. It is the sole gate: nothing else
// in this package parses a virtual path, and no operation accepts one.
//
// Paths outside every grant produce ErrNotFound, identical byte for byte to a
// path simply absent from disk. Returning a denial would confirm to a stranger
// that the path exists, so the existence rule applies in exactly one place and a
// denial is reachable only by a caller whose label already matched a grant.
//
// No stat occurs here. Existence is the consuming operation's concern, and its
// answer for a missing path is the same ErrNotFound. That is the point: which
// layer produced the rejection is not observable.
func (c *Core) Resolve(user UserID, p vfs.Vpath, need acl.Perms) (Resolved, error) {
	if p.IsRoot() {
		// The virtual root names no share. It is listed through Roots and
		// never resolved.
		return Resolved{}, ErrNotFound
	}

	// Eager, because the projected root is built from grants: without this
	// hook a home appears only after an access that cannot happen until the
	// home is in the root. Best-effort, because a home hiccup must not break
	// access to the user's other shares.
	if err := c.ensureHome(context.Background(), user); err != nil {
		c.warn("creating a home directory failed; the user's other shares are unaffected",
			"user", int64(user), "error", err)
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
	for _, r := range c.labelledRoots(user) {
		if r.Label == label {
			return r, true
		}
	}
	return acl.RootEntry{}, false
}

// joinSubpath prefixes the grant's subpath onto the caller's remaining path.
//
// JoinExisting is used rather than Join because resolution addresses a path
// without creating one. The creation table rejects names an SMB client could
// never open, which is correct for a name this server is about to create and
// wrong for one another program already wrote; a directory actually named CON
// must remain listable and resolvable.
//
// A join failure on a grant subpath component indicates a corrupt grant, and the
// resolution is rejected rather than the subpath truncated: a grant unable to
// express its own scope must not resolve to a broader one.
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
	at := acl.Vpath{Share: int64(parent.share), Path: aclPath(p)}
	effective := c.acl.Effective(int64(parent.user), at) & parent.perms
	if !effective.Has(need) {
		return Resolved{}, ErrDenied
	}
	return Resolved{
		user:  parent.user,
		share: parent.share,
		root:  parent.root,
		path:  p,
		perms: effective,
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
// as. It answers "what is the URL of this" for search hits, recent listings,
// download links and WebDAV hrefs.
//
// A share the user cannot see is an error rather than a guessed label,
// because a URL under a label they do not hold is a URL to nothing.
//
// The grant subpath comes off the front. A path inside this package is always
// share-relative and carries it; the projected space the label roots does not,
// because the label is what stands for the subpath. Joining without stripping
// produced "Game/Game/file" for an account granted one folder, which is a URL
// to a file that does not exist, at whatever depth the grant sits.
//
// The deepest matching root wins. One account can hold two grants on one
// share, each projected under its own label, and the answer for a path is the
// one whose subpath actually contains it.
func (c *Core) VpathFor(user UserID, share ShareID, p vfs.SharePath) (vfs.Vpath, error) {
	comps := p.Components()

	depth, label := -1, ""
	for _, r := range c.labelledRoots(user) {
		narrowed, err := num.Narrow[uint32](r.Share)
		if err != nil || ShareID(narrowed) != share {
			continue
		}
		sub := r.Subpath.Components()
		if len(sub) <= depth || !componentsPrefix(sub, comps) {
			continue
		}
		depth, label = len(sub), r.Label
	}
	if depth < 0 {
		return vfs.Vpath{}, errors.New("vpath for an unreadable share")
	}

	rest, err := vfs.ParseSharePath(strings.Join(comps[depth:], "/"))
	if err != nil {
		return vfs.Vpath{}, err
	}
	return vfs.NewVpath(label, rest)
}

// componentsPrefix reports whether sub names the first components of p.
//
// Component-wise, never a string prefix: "Gamera" starts with "Game" and is a
// different folder, so a byte comparison would project a path under a label
// that does not contain it.
func componentsPrefix(sub, p []string) bool {
	if len(sub) > len(p) {
		return false
	}
	for i := range sub {
		if sub[i] != p[i] {
			return false
		}
	}
	return true
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

// requireCreatableLeaf applies the creation table to a leaf about to exist, by
// rejoining it onto its parent and discarding the outcome. The root passes,
// since nothing there is created by name.
//
// This is the counterpart to joinSubpath's asymmetry: everything already present
// on the share stays fully usable, while nothing entered through this server
// introduces a name a Windows or SMB client could never open.
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
