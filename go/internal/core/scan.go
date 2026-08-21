package core

import (
	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// ScanSources is every share, for a measurement that sizes an index.
//
// Administrator-scoped and deliberately so: the index covers every share, so
// sizing it against one account's view would report a number the built index
// does not match. The caller is the one place that checks who is asking.
func (c *Core) ScanSources() []search.Source {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()

	out := make([]search.Source, 0, len(c.shares))
	for _, e := range c.shares {
		id, err := num.Narrow[uint32](int64(e.def.ID))
		if err != nil {
			continue
		}
		out = append(out, search.Source{
			Share: id,
			Root:  e.root,
			Base:  vfs.RootPath(),
		})
	}
	return out
}

// ShareLabel is the name this account navigates a share under, which is the
// grant's label rather than the share's own name.
//
// Empty when the account cannot see the share at all. A caller rendering a hit
// has already checked that they can, so an empty answer there means the grant
// went away between the search and the render.
func (c *Core) ShareLabel(user UserID, share uint32) string {
	for _, r := range c.acl.Roots(int64(user)) {
		id, err := num.Narrow[uint32](r.Share)
		if err != nil {
			continue
		}
		if id == share {
			return r.Label
		}
	}
	return ""
}

// UserScanSources is the same, narrowed to what one account may read.
//
// The permission check runs per entry rather than per share, because a grant
// can start partway down a tree and a share-level answer would either hide a
// readable subtree or count an unreadable one.
func (c *Core) UserScanSources(user UserID) []search.Source {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()

	var out []search.Source
	for _, e := range c.shares {
		id, err := num.Narrow[uint32](int64(e.def.ID))
		if err != nil {
			continue
		}
		share := int64(e.def.ID)
		out = append(out, search.Source{
			Share: id,
			Root:  e.root,
			Base:  vfs.RootPath(),
			Allow: func(p vfs.SafePath, _ bool) bool {
				return c.acl.Evaluate(int64(user), acl.Vpath{Share: share, Path: acl.NewPath(p.Components()...)}, acl.Read).Allowed
			},
		})
	}
	return out
}
