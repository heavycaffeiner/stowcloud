//go:build linux

// Package core is the protocol-agnostic domain API every protocol sits on.
//
// It must not know any of them exist, which an import graph rather than a text
// search now enforces: nothing in this package may import a protocol package,
// and the gate scans the import graph to say so.
//
// The one thing that makes the ACL gate unskippable lives here: Resolve is the
// only place the existence rule is applied, and Resolved cannot be constructed
// outside this package. Every operation below takes a Resolved, never a
// virtual path, so there is no way to reach a mutation without first having
// gone through the permission check.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// UserID is an account, as the auth layer addresses one. It is opaque to this
// package: grants name a user id, never a username.
type UserID int64

// Options is what New cannot work out from the store.
type Options struct {
	// ACL is the permission engine. It must be loaded with this user's
	// grants and memberships before Resolve is called.
	ACL *acl.Evaluator

	// Clock stamps journal rows and audit-style timestamps. Nil takes the
	// system clock.
	Clock clock.Clock
}

// Core is the domain root. It is cheap to construct and threadsafe once built.
type Core struct {
	cache    *cache.DB
	state    *state.DB
	journal  *journal.DB
	acl      *acl.Evaluator
	clk      clock.Clock
	logger   *slog.Logger
	quota    QuotaSink
	sharesMu sync.RWMutex
	shares   map[ShareID]*shareEntry
	homeOnce sync.Mutex

	// linkCipher and verifyLinkPw are the at-rest seam for share links,
	// attached by the server from the loaded master key. A Core without them
	// cannot mint or open a token and fails a check rather than passing one.
	linkCipher   LinkCipher
	verifyLinkPw passwordVerifier
}

// shareEntry is one registered share: its definition and the live root.
type shareEntry struct {
	def  ShareDef
	root *vfs.ShareRoot
}

// New wires a Core over the store. nil is refused for every field that has no
// sensible zero value, so a partially built Core cannot be mistaken for a
// working one.
func New(s *store.Store, opt Options) (*Core, error) {
	if s == nil {
		return nil, errors.New("core requires a store")
	}
	if opt.ACL == nil {
		return nil, errors.New("core requires an ACL evaluator")
	}
	clk := opt.Clock
	if clk == nil {
		clk = clock.System()
	}
	return &Core{
		cache:   s.Cache(),
		state:   s.State(),
		journal: s.Journal(),
		acl:     opt.ACL,
		clk:     clk,
		logger:  slog.Default(),
		shares:  map[ShareID]*shareEntry{},
	}, nil
}

// ShareID aliases the VFS share id, which is the only id scheme this package
// recognises a share by.
type ShareID = vfs.ShareID

// ShareDef is one configured share as the admin-facing API sees it. Nothing
// here opens or validates the host path; registration is the config layer's
// boundary, and this is the fact it passes in.
type ShareDef struct {
	ID   ShareID
	Name string
	// Host is the on-disk path. It is trusted server-side configuration and
	// must never reach a client response.
	Host string
	// Policy carries the symlink, mode and ownership decisions.
	Policy vfs.SharePolicy
	// TrashEnabled is the admin-visible toggle, reported in the listing so a
	// delete confirmation can say whether the delete is undoable.
	TrashEnabled bool
	// SharedExternally marks a share another service also reads, which the
	// client renders as a badge.
	SharedExternally bool
}

// RegisterShare opens host as a share root and remembers it. Re-registering an
// id that is already registered replaces the live root, which is what a restart
// does when persisted shares are reloaded.
func (c *Core) RegisterShare(ctx context.Context, def ShareDef) error {
	root, err := vfs.OpenShareRoot(def.ID, def.Host, def.Policy)
	if err != nil {
		return err
	}
	c.sharesMu.Lock()
	defer c.sharesMu.Unlock()
	c.shares[def.ID] = &shareEntry{def: def, root: root}
	return nil
}

// ShareRoot returns the live root for id, and false if it is not registered.
func (c *Core) ShareRoot(id ShareID) (*vfs.ShareRoot, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok {
		return nil, false
	}
	return e.root, true
}

// Share returns the definition for id, and false if it is not registered.
func (c *Core) Share(id ShareID) (ShareDef, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok {
		return ShareDef{}, false
	}
	return e.def, true
}

// Shares lists every registered share in id order.
func (c *Core) Shares() []ShareDef {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	out := make([]ShareDef, 0, len(c.shares))
	for _, e := range c.shares {
		out = append(out, e.def)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Roots is the user's virtual root: one entry per readable share, labeled by
// the grant. It is what a client renders as the top-level folder list.
func (c *Core) Roots(user UserID) []acl.RootEntry {
	if err := c.ensureHome(context.Background(), user); err != nil {
		c.warn("home creation failed; listing without it", "error", err)
	}
	roots := c.acl.Roots(int64(user))
	for i := range roots {
		s, nerr := num.Narrow[uint32](roots[i].Share)
		if nerr != nil {
			continue
		}
		if e, ok := c.shareDef(ShareID(s)); ok {
			roots[i].TrashEnabled = e.TrashEnabled
			roots[i].SharedExternally = e.SharedExternally
		}
	}
	return roots
}

func (c *Core) shareDef(id ShareID) (ShareDef, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok {
		return ShareDef{}, false
	}
	return e.def, true
}

// warn logs a failure that must not fail the operation that hit it. The
// patterns this package uses it for are all of the same shape: the write
// already succeeded, and the bookkeeping after it is best-effort.
func (c *Core) warn(msg string, key string, val any) {
	c.logger.Warn(msg, slog.Any(key, val))
}

// errf wraps a sentinel with context.
func errf(wrap error, format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, wrap)...)
}
