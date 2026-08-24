//go:build linux

package core

import (
	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The two helpers a recursive protocol walk needs.
//
// A walk holds a Resolved for a directory and has to produce one for a child
// it just listed. Re-resolving from a virtual path would re-run grant matching
// per entry, which is the per-entry path resolution the whole design avoids;
// it would also be wrong, because the child's virtual path is not reconstructible
// once a grant subpath is on the front.

// EntryAt is the projection of the resolved path itself.
//
// List produces entries for a directory's children. A protocol that reports on
// the directory as well needs the same shape for it, and building one by hand
// outside this package is impossible: Entry carries an identity and a
// validator that only the core mints.
func (c *Core) EntryAt(r Resolved, st vfs.Stat) Entry {
	etag, weak := FileETag(st)
	return Entry{
		Name:     r.path.Name(),
		Path:     r.path.Share(),
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

// ResolveUnder narrows a resolution onto a path beneath it.
//
// The permissions come from the parent rather than from a fresh grant lookup,
// which is correct because a grant covers a subtree: a child of a granted
// directory is under the same grant by construction. need is checked against
// them, so a caller cannot widen its own access by descending.
//
// The path must be under the parent's. That is the whole safety argument, and
// it is checked rather than assumed.
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
