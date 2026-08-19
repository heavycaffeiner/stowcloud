//go:build linux

package core

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Resolved is what every operation below takes instead of a virtual path. It
// holds the share root, the validated path under it (grant subpath already on
// the front), and the permissions the caller holds there.
//
// The fields are unexported, which is the whole of the ACL guarantee: no
// caller can construct a Resolved by hand and hand it to an operation, because
// the only way to obtain one is through Resolve, which is the only place the
// permission check is applied.
type Resolved struct {
	user  UserID
	share ShareID
	root  *vfs.ShareRoot
	path  vfs.SafePath
	perms acl.Perms
}

// Share is the share this resolution landed in.
func (r Resolved) Share() ShareID { return r.share }

// Root is the live share root.
func (r Resolved) Root() *vfs.ShareRoot { return r.root }

// Path is the validated share-relative path, grant subpath on the front.
func (r Resolved) Path() vfs.SafePath { return r.path }

// Perms is the caller's full effective permission set at Path.
func (r Resolved) Perms() acl.Perms { return r.perms }

// User is the caller this resolution was made for.
func (r Resolved) User() UserID { return r.user }

// Has reports whether the caller holds every bit in want at this path.
func (r Resolved) Has(want acl.Perms) bool { return r.perms.Has(want) }

// Require is a check on a Resolved already in hand: the caller re-checks a
// permission that the resolution's single want bit did not cover. It exists
// so an operation can demand, say, both READ and DOWNLOAD with one gate.
func (r Resolved) Require(want acl.Perms) error {
	if r.perms.Has(want) {
		return nil
	}
	return ErrDenied
}

// Resolve turns a client path into a share root, a validated path under it,
// and the permissions the caller holds there.
//
// A path outside every grant returns ErrNotFound, identical to a path that
// does not exist: returning a denial tells a stranger the path exists, so the
// existence rule is applied in exactly one place and a 403 can only be earned
// by a caller who may know the target exists (the label matched a grant, but
// the permission under it did not).
//
// This is the single entry point to the virtual root. Nothing else in this
// package parses a virtual path, and no operation below accepts one.
func (c *Core) Resolve(user UserID, p vfs.Vpath, need acl.Perms) (Resolved, error) {
	if p.IsRoot() {
		return Resolved{}, ErrNotFound
	}

	// Homes are a grant that is implied by the account rather than created by
	// an administrator. Eager, best-effort: a home hiccup must not break the
	// user's other shares, so a failure is logged and resolution continues.
	if err := c.ensureHome(context.Background(), user); err != nil {
		c.warn("home creation failed; resolving without it", "error", err)
	}

	label := p.Label()
	rest := p.Rest()

	// The label is looked up in the caller's own projected root. A label the
	// caller has no grant over, or one that names no registered share, is a
	// missing path, never a denial.
	var match acl.RootEntry
	found := false
	for _, r := range c.acl.Roots(int64(user)) {
		if r.Label == label {
			match = r
			found = true
			break
		}
	}
	if !found {
		return Resolved{}, ErrNotFound
	}

	share64, nerr := num.Narrow[uint32](match.Share)
	if nerr != nil {
		return Resolved{}, errf(ErrNotFound, "the grant table holds a share id that does not fit")
	}
	shareID := ShareID(share64)
	entry, ok := c.shareEntry(shareID)
	if !ok {
		return Resolved{}, ErrNotFound
	}

	full, err := c.joinSubpath(entry.def, match.Subpath, rest)
	if err != nil {
		return Resolved{}, err
	}

	// The permission check. A denied path under a share the caller may know
	// exists is a denial; one outside every grant already returned NotFound
	// above.
	decision := c.acl.Evaluate(int64(user), acl.Vpath{Share: match.Share, Path: aclPath(full)}, need)
	if !decision.Allowed {
		return Resolved{}, ErrDenied
	}

	perms := c.acl.Effective(int64(user), acl.Vpath{Share: match.Share, Path: aclPath(full)})
	return Resolved{
		user:  user,
		share: shareID,
		root:  entry.root,
		path:  full,
		perms: perms,
	}, nil
}

func (c *Core) shareEntry(id ShareID) (*shareEntry, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	return e, ok
}

// joinSubpath lays the grant's subpath on the front of the caller's rest
// path, using the existing-name validation table. The grant subpath and the
// client rest are joined with JoinExisting rather than Join: resolution
// addresses a path, it does not create one, and the creation table (which
// refuses CON and a:b) has no business deciding whether a path already on
// disk can be named.
func (c *Core) joinSubpath(def ShareDef, subpath acl.Path, rest vfs.SharePath) (vfs.SafePath, error) {
	full := vfs.RootPath()
	// The grant subpath is trusted configuration, but a reserved or invalid
	// component in it is a corrupt grant, not a path to silently truncate:
	// a grant that cannot name its own scope must refuse the resolution.
	for _, comp := range subpath.Components() {
		var jerr error
		full, jerr = full.JoinExisting(comp)
		if jerr != nil {
			return vfs.SafePath{}, jerr
		}
	}
	if rest.IsRoot() {
		return full, nil
	}
	restSafe, err := rest.Safe()
	if err != nil {
		return vfs.SafePath{}, err
	}
	for _, comp := range restSafe.Components() {
		var jerr error
		full, jerr = full.JoinExisting(comp)
		if jerr != nil {
			return vfs.SafePath{}, jerr
		}
	}
	return full, nil
}

// aclPath is the crossing from the validated share-relative path this package
// uses to the ACL engine's own component path.
func aclPath(p vfs.SafePath) acl.Path {
	return acl.NewPath(p.Components()...)
}

// VpathFor is the crossing back out of the core's vocabulary: a resolved path
// into the form a client sees, under the share's label. It exists for the
// protocol layers that have to answer "what is the URL of this" and is the
// inverse of Resolve.
func (c *Core) VpathFor(user UserID, share ShareID, p vfs.SharePath) (vfs.Vpath, error) {
	label := ""
	for _, r := range c.acl.Roots(int64(user)) {
		rs, nerr := num.Narrow[uint32](r.Share)
		if nerr != nil {
			continue
		}
		if ShareID(rs) == share {
			label = r.Label
			break
		}
	}
	if label == "" {
		return vfs.Vpath{}, errors.New("vpath for an unreadable share")
	}
	return vfs.NewVpath(label, p)
}
