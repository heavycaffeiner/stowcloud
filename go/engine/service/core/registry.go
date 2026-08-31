//go:build linux

package core

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// Share is one configured share as the admin-facing API returns it.
type Share struct {
	ID   ShareID
	Name string

	// Host is the on-disk path. Trusted server-side configuration; it must
	// never reach a client response.
	Host string

	// Policy holds the symlink, mode and ownership decisions.
	Policy vfs.SharePolicy

	// TrashEnabled is the admin-visible toggle, reported in the listing so
	// a delete confirmation can say whether the delete is undoable.
	TrashEnabled bool

	// SharedExternally flags a share that another service also reads, which the
	// client displays as a badge.
	SharedExternally bool

	// BrokenReason is a token explaining why this share is currently unservable,
	// empty when it is servable. It draws on the health surface's vocabulary, so
	// a screen and a probe asking the same question receive the same word.
	BrokenReason string
}

// ShareDef is the internal spelling of Share, kept a distinct name so the
// config layer's registration surface is not the admin API's.
type ShareDef = Share

// ShareSpec describes what CreateShare is asked to produce.
type ShareSpec struct {
	Name string

	// Host is the on-disk path. Trusted server-side configuration; it must
	// never reach a client response.
	Host string
}

// SharePatch is what UpdateShare accepts. Pointers separate an absent field from
// a cleared one, which is the difference between leaving trash untouched and
// turning it off.
type SharePatch struct {
	Name         *string
	Host         *string
	TrashEnabled *bool
}

// shareEntry pairs a registered share's definition with its live root.
//
// root is nil for a broken share, and the entry remains in the map regardless.
// Removing it is what made a disk that never returned indistinguishable from a
// share someone deleted: missing from the admin list, missing from every
// account's roots, leaving only a line on a health endpoint.
type shareEntry struct {
	def       ShareDef
	root      *vfs.ShareRoot
	brokenErr error
}

// RegisterShare opens the definition's host as a share root and remembers
// it.
//
// The filesystem admission gate runs here, so a share this design cannot
// hold its contracts on is refused at registration rather than at the first
// operation that cannot keep them. Re-registering an id that is already
// registered replaces the entry, which is what reload, retry and edit all
// do.
func (c *Core) RegisterShare(ctx context.Context, def ShareDef) error {
	root, adm, err := vfs.RegisterShareRoot(def.ID, def.Host, def.Policy)
	if err != nil {
		return err
	}
	if adm.Warn != "" {
		c.warn("share admitted with a caveat",
			slog.String("share", def.Name), slog.String("warning", adm.Warn))
	}
	def.BrokenReason = ""
	c.replaceEntry(&shareEntry{def: def, root: root})
	return nil
}

// RegisterBroken remembers a share whose root would not open, marked with
// why.
func (c *Core) RegisterBroken(def ShareDef, cause error) {
	def.BrokenReason = RejectionKind(cause)
	c.replaceEntry(&shareEntry{def: def, brokenErr: cause})
}

// replaceEntry installs an entry and closes the root held by its predecessor.
//
// Closing is what makes repeated re-registration safe: both retry and edit pass
// through here, and without it every attempt would leak the descriptor opened by
// the last. The close runs after the lock is released, since it is a syscall
// that must not serialize every registry read behind it, and by then the entry
// is already unreachable through the map.
func (c *Core) replaceEntry(e *shareEntry) {
	c.sharesMu.Lock()
	old, had := c.shares[e.def.ID]
	c.shares[e.def.ID] = e
	c.sharesMu.Unlock()

	if !had || old.root == nil || old.root == e.root {
		return
	}
	if err := old.root.Close(); err != nil {
		c.warn("closing a replaced share's root failed",
			slog.String("share", old.def.Name), slog.Any("error", err))
	}
}

// ShareBroken explains why a share is unservable, returning nil when it is
// servable.
//
// Unregistered ids also yield nil: such a share is absent rather than broken,
// and callers needing to tell the two apart get false from the accessors.
func (c *Core) ShareBroken(id ShareID) error {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok {
		return nil
	}
	return e.brokenErr
}

// ProbeShares revalidates every registered share, moving each between live and
// broken, and returns only those that changed.
//
// Handling both directions is what justifies running this on a schedule. A root
// whose filesystem was unmounted beneath it retains a descriptor that opens
// nothing, so absent the probe the share fails one request at a time with
// nothing noticing. A broken share whose disk returned must resume working
// without anyone intervening.
//
// Retrying performs a complete re-registration rather than a re-open, so the
// admission gate runs again: a path that returned on a filesystem this server
// rejects remains broken.
func (c *Core) ProbeShares(ctx context.Context) (broke, healed []ShareDef) {
	for _, def := range c.Shares() {
		if c.ShareBroken(def.ID) != nil {
			if err := c.RegisterShare(ctx, def); err != nil {
				continue
			}
			def.BrokenReason = ""
			healed = append(healed, def)
			continue
		}

		root, ok := c.ShareRoot(def.ID)
		if !ok {
			continue
		}
		if err := root.Alive(); err != nil {
			c.RegisterBroken(def, err)
			def.BrokenReason = RejectionKind(err)
			broke = append(broke, def)
		}
	}
	return broke, healed
}

// UnregisterShare ceases serving a share and closes its root.
//
// The root is closed rather than merely discarded, since it is an open directory
// descriptor and a deployment adding and removing shares over its lifetime would
// otherwise leak one per removal. Broken entries have no root, and the nil check
// carries weight: dereferencing it is what made removing a share whose disk had
// disappeared return a 500, leaving the one share nothing will re-probe stuck in
// permanent degradation.
func (c *Core) UnregisterShare(id ShareID) {
	c.sharesMu.Lock()
	e, ok := c.shares[id]
	delete(c.shares, id)
	c.sharesMu.Unlock()

	if !ok || e.root == nil {
		return
	}
	if err := e.root.Close(); err != nil {
		c.warn("closing a removed share's root failed",
			slog.String("share", e.def.Name), slog.Any("error", err))
	}
}

// ShareRoot is the live root for an id, and false when it is unregistered or
// broken. A broken share hands out no root; handing out a nil one would move
// the failure to whoever dereferenced it.
func (c *Core) ShareRoot(id ShareID) (*vfs.ShareRoot, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok || e.root == nil {
		return nil, false
	}
	return e.root, true
}

// shareEntry is the package-internal lookup Resolve uses, handing back the
// entry itself rather than a copy: the resolver needs the live root and the
// broken cause together, and reading them through two accessors could see
// two different registrations.
func (c *Core) shareEntry(id ShareID) (*shareEntry, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	return e, ok
}

// Share is the definition for a registered id, broken or not.
func (c *Core) Share(id ShareID) (ShareDef, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok {
		return ShareDef{}, false
	}
	return e.def, true
}

// Shares lists every registered definition, broken included, by ascending
// id.
func (c *Core) Shares() []ShareDef {
	c.sharesMu.RLock()
	out := make([]ShareDef, 0, len(c.shares))
	for _, e := range c.shares {
		out = append(out, e.def)
	}
	c.sharesMu.RUnlock()

	slices.SortFunc(out, func(a, b ShareDef) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	return out
}

// Roots is the user's virtual root: one entry per readable grant, labeled as
// the evaluator projects it, with the registry's own facts filled in.
//
// A broken share stays in the listing carrying why. Dropping it is what made
// a share disappear from the browser with no explanation anywhere a user
// could see.
func (c *Core) Roots(user UserID) []acl.RootEntry {
	// The same eager, best-effort hook Resolve runs. It belongs here too:
	// this listing is what a client draws as the top-level folder list, and
	// without it a new account sees no home until some later call happens to
	// resolve one.
	if err := c.ensureHome(context.Background(), user); err != nil {
		c.warn("creating a home directory failed; listing the other shares anyway",
			"user", int64(user), "error", err)
	}
	roots := c.acl.Roots(int64(user))
	for i := range roots {
		id, err := num.Narrow[uint32](roots[i].Share)
		if err != nil {
			continue
		}
		def, ok := c.Share(ShareID(id))
		if !ok {
			continue
		}
		roots[i].TrashEnabled = def.TrashEnabled
		roots[i].SharedExternally = def.SharedExternally
		roots[i].BrokenReason = def.BrokenReason
	}
	return roots
}

// RejectionKind is the health surface's token for why a share would not
// register. It is exported because the assembly layer registers shares too
// and carries the same tokens onto the health surface.
func RejectionKind(err error) string {
	var adm *vfs.AdmissionError
	switch {
	case errors.As(err, &adm):
		return adm.Type.String()
	case errors.Is(err, vfs.ErrNotFound):
		return "missing"
	case errors.Is(err, vfs.ErrDenied):
		return "unreadable"
	default:
		return "unavailable"
	}
}
