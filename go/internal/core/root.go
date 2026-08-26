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
	"crypto/rand"
	"encoding/hex"
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
	hashLinkPw   passwordHasher
	verifyLinkPw passwordVerifier
}

// shareEntry is one registered share: its definition and the live root.
//
// root is nil when the share is broken. A broken share is still an entry, and
// that is the point: it used to be dropped, so a folder whose path disappeared
// vanished from the admin screen and from every user's root list, which reads
// as a share somebody deleted rather than a disk that did not come back. It
// stays listed, marked, with the reason attached.
type shareEntry struct {
	def  ShareDef
	root *vfs.ShareRoot
	// brokenErr is why the root could not be opened, or nil when it is live.
	brokenErr error
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
	c := &Core{
		cache:   s.Cache(),
		state:   s.State(),
		journal: s.Journal(),
		acl:     opt.ACL,
		clk:     clk,
		logger:  slog.Default(),
		shares:  map[ShareID]*shareEntry{},
	}
	// The ACL gate is the whole permission story, and it is loaded from the
	// durable grants at boot: a core that does not know its own grants is a
	// core that has silently denied everything since it started.
	if err := c.acl.LoadFromState(context.Background(), c.state.SQL()); err != nil {
		return nil, fmt.Errorf("loading grants: %w", err)
	}
	return c, nil
}

// ShareID aliases the VFS share id, which is the only id scheme this package
// recognises a share by.
type ShareID = vfs.ShareID

// Share is one configured share as the admin-facing API returns it. Nothing
// here opens or validates the host path; registration is the config layer's
// boundary, and this is the fact it passes in.
type Share struct {
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

	// BrokenReason is a token naming why this share cannot be served right
	// now, or empty when it can. It is the same vocabulary the health surface
	// carries ("missing", "unreadable", "unavailable"), because a screen and a
	// probe asking the same question should get the same word back.
	BrokenReason string
}

// ShareDef is the internal spelling of Share, kept separate so the config
// layer's registration surface is not the admin API's.
type ShareDef = Share

// ShareSpec is what CreateShare is asked to mint.
type ShareSpec struct {
	Name string
	// Host is the on-disk path. It is trusted server-side configuration and
	// must never reach a client response.
	Host string
}

// SharePatch is what UpdateShare accepts. The pointers distinguish "field
// absent" from "field cleared", which is exactly the difference between
// leaving trash alone and disabling it.
type SharePatch struct {
	Name         *string
	Host         *string
	TrashEnabled *bool
}

// RegisterShare opens host as a share root and remembers it. Re-registering an
// id that is already registered replaces the live root, which is what a restart
// does when persisted shares are reloaded.
func (c *Core) RegisterShare(ctx context.Context, def ShareDef) error {
	// The filesystem gate runs here, so a share this design cannot hold its
	// contracts on is refused at registration rather than at the first
	// operation that cannot keep them.
	root, adm, err := vfs.RegisterShareRoot(def.ID, def.Host, def.Policy)
	if err != nil {
		return err
	}
	if adm.Warn != "" {
		c.logger.Warn("share admitted with a caveat", "share", def.Name, "warning", adm.Warn)
	}
	def.BrokenReason = ""
	c.replaceEntry(&shareEntry{def: def, root: root})
	return nil
}

// RegisterBroken remembers a share whose root would not open.
//
// The alternative is what this replaces: the share was left out of the
// registry, so it was absent from the admin list, absent from every user's
// roots, and present only as a line on the health endpoint. From the interface
// that is indistinguishable from a share nobody ever created, which is the
// worst thing a missing disk can look like.
func (c *Core) RegisterBroken(def ShareDef, cause error) {
	def.BrokenReason = RejectionKind(cause)
	c.replaceEntry(&shareEntry{def: def, brokenErr: cause})
}

// replaceEntry installs an entry, closing the root the previous one held.
//
// Closing is what makes re-registration safe to call repeatedly: retry and
// edit both go through it, and without this each attempt would leak the
// descriptor the last one opened.
func (c *Core) replaceEntry(e *shareEntry) {
	c.sharesMu.Lock()
	old, had := c.shares[e.def.ID]
	c.shares[e.def.ID] = e
	c.sharesMu.Unlock()
	if had && old.root != nil && old.root != e.root {
		if err := old.root.Close(); err != nil {
			c.logger.Warn("closing a replaced share's root failed",
				slog.String("share", old.def.Name), slog.Any("error", err))
		}
	}
}

// ShareBroken is why a share cannot be served, or nil when it can.
func (c *Core) ShareBroken(id ShareID) error {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok {
		return nil
	}
	return e.brokenErr
}

// ProbeShares re-checks every registered root and moves shares between live
// and broken.
//
// Both directions, which is what makes it worth running on a schedule. A root
// whose filesystem was unmounted underneath it keeps a descriptor that opens
// nothing, so the share fails one request at a time and nothing notices; a
// broken share whose disk came back has to start working again without
// somebody pressing anything.
//
// It returns what changed, so the caller can log a transition rather than the
// steady state: a probe that logged every pass would bury the one line that
// matters under a line a minute.
func (c *Core) ProbeShares(ctx context.Context) (broke, healed []ShareDef) {
	for _, def := range c.Shares() {
		switch alive := c.ShareBroken(def.ID) == nil; {
		case alive:
			root, ok := c.ShareRoot(def.ID)
			if !ok {
				continue
			}
			if err := root.Alive(); err != nil {
				c.RegisterBroken(def, err)
				def.BrokenReason = RejectionKind(err)
				broke = append(broke, def)
			}
		default:
			// Retried by re-registering, which runs the whole admission gate
			// again rather than only re-opening: a path that came back on a
			// filesystem this server will not serve is still broken, and
			// finding that out here beats finding it out per request.
			if err := c.RegisterShare(ctx, def); err == nil {
				def.BrokenReason = ""
				healed = append(healed, def)
			}
		}
	}
	return broke, healed
}

// UnregisterShare stops serving a share and closes its root.
//
// The root is closed rather than dropped: it is an open descriptor on a
// directory, and a deployment that adds and removes shares over its life would
// otherwise leak one per removal. An in-flight request holding the same root
// keeps its own reference, so the close lands when that reference goes.
func (c *Core) UnregisterShare(id ShareID) {
	c.sharesMu.Lock()
	e, ok := c.shares[id]
	delete(c.shares, id)
	c.sharesMu.Unlock()
	// A broken share has no root to close. Dereferencing one is what made
	// removing a share whose disk had gone answer 500, which left the only
	// share nothing will ever re-probe stuck as a permanent degradation.
	if !ok || e.root == nil {
		return
	}
	if err := e.root.Close(); err != nil {
		c.logger.Warn("closing a removed share's root failed",
			slog.String("share", e.def.Name), slog.Any("error", err))
	}
}

// ShareRoot returns the live root for id, and false if it is not registered
// or is broken. A broken share has no root to hand out, and handing out a nil
// one would move the failure to whoever dereferenced it.
func (c *Core) ShareRoot(id ShareID) (*vfs.ShareRoot, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok || e.root == nil {
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
			// The root stays in the list when its disk is gone, carrying why.
			// Dropping it is what made a share disappear from the browser with
			// no explanation anywhere a user could see.
			roots[i].BrokenReason = e.BrokenReason
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

// NewInstanceID mints the identity a deployment presents to clients.
//
// It lives in the core rather than in whichever layer first needs one, because
// it is a property of this deployment and not of a protocol. Minted once and
// never regenerated: a client that saw one identity and then another treats
// the server as a different server and re-syncs everything it holds.
func NewInstanceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("core: minting an instance id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
