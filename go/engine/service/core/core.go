//go:build linux

package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Options is everything New cannot work out for itself. The three databases
// are separate fields rather than one bundle because they have separate
// lifetimes: the cache is deletable, the journal is optional, and the state
// database is the only one whose absence is fatal.
type Options struct {
	// State is the durable half: shares, grants, links, quota. Required.
	State *state.DB

	// Cache is the rebuildable half: identities and directory aggregates.
	// Required, because every listing mints identities from it.
	Cache *cache.DB

	// Journal records what an account wrote, for the recent listing. Nil is
	// allowed: a journal that would not open costs the recent listing and
	// nothing else, and the write path must not fail because a
	// nice-to-have database is missing.
	Journal *journal.DB

	// ACL is the permission evaluator. Required, and loaded from the
	// durable grants during New: a core that does not know its own grants
	// has silently denied everything since it started.
	ACL *acl.Evaluator

	// Links is the store's share_link implementation. Nil is allowed at
	// construction and fails every link operation with a wiring error, so a
	// deployment that never wired it is told rather than crashing mid-mint.
	Links LinkStore

	// Clock stamps rows and timestamps. Nil takes the system clock.
	Clock clock.Clock

	// Logger receives the best-effort failures the domain must not fail an
	// operation over. Nil takes the default logger.
	Logger *slog.Logger
}

// Core is the domain root: cheap to construct, threadsafe once built.
type Core struct {
	state   *state.DB
	cache   *cache.DB
	journal *journal.DB
	acl     *acl.Evaluator
	clk     clock.Clock
	logger  *slog.Logger

	// The link seams, wired after construction because the cipher needs a
	// master key the server loads later. Each one fails closed when unwired.
	linkStore    LinkStore
	linkCipher   LinkCipher
	hashLinkPw   passwordHasher
	verifyLinkPw passwordVerifier

	// quota is the per-user byte ledger, attached after construction
	// because a deployment without one is legitimate and because the
	// implementation lives in the store layer.
	quota QuotaSink

	sharesMu sync.RWMutex
	shares   map[ShareID]*shareEntry

	// homeOnce serializes the once-per-user home creation. Only the slow
	// path takes it; the steady state reads the grant marker and returns.
	homeOnce sync.Mutex
}

// New wires a Core over the store and loads the grant table into the
// evaluator.
//
// Every field with no sensible zero value is refused when nil, so a
// half-wired Core cannot be mistaken for a working one. The one exception is
// the journal, whose absence is a documented degradation rather than a
// wiring mistake.
func New(ctx context.Context, opt Options) (*Core, error) {
	switch {
	case opt.State == nil:
		return nil, errors.New("core requires a state database")
	case opt.Cache == nil:
		return nil, errors.New("core requires a cache database")
	case opt.ACL == nil:
		return nil, errors.New("core requires an ACL evaluator")
	}

	clk := opt.Clock
	if clk == nil {
		clk = clock.System()
	}
	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}

	c := &Core{
		state:   opt.State,
		cache:   opt.Cache,
		journal: opt.Journal,
		acl:     opt.ACL,
		clk:     clk,
		logger:  logger,
		shares:  map[ShareID]*shareEntry{},
	}
	c.linkStore = opt.Links
	if err := c.ReloadGrants(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// ReloadGrants replaces the evaluator's grants and memberships from the
// durable rows.
//
// The conversion lives here because the two sides speak different
// vocabularies on purpose: the state package owns the row shape, the ACL
// package owns the domain shape, and neither imports the other. Every write
// that changes a grant calls this afterwards, so a grant and the evaluator
// serving it cannot drift apart.
func (c *Core) ReloadGrants(ctx context.Context) error {
	rows, err := c.state.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		return fmt.Errorf("loading grants: %w", err)
	}
	members, err := c.state.Memberships(ctx)
	if err != nil {
		return fmt.Errorf("loading memberships: %w", err)
	}

	grants := make([]acl.Grant, 0, len(rows))
	for _, r := range rows {
		grants = append(grants, grantOf(r))
	}
	memberships := make([]acl.Membership, 0, len(members))
	for _, m := range members {
		memberships = append(memberships, acl.Membership{User: m.User, Group: m.Group})
	}
	if err := c.acl.LoadFromState(grants, memberships); err != nil {
		return fmt.Errorf("loading grants: %w", err)
	}
	return nil
}

// grantOf converts one stored row into the evaluator's domain value. A row
// names exactly one of a user and a group, which the store validates on the
// way in, so a nil pointer here reads as zero and the other field carries
// the holder.
func grantOf(r state.GrantRow) acl.Grant {
	g := acl.Grant{
		ID:        r.ID,
		Share:     r.Share,
		Subpath:   acl.ParsePath(r.Subpath),
		Allow:     acl.Perms(r.Allow),
		Deny:      acl.Perms(r.Deny),
		Inherit:   r.Inherit,
		Label:     r.Label,
		CreatedNs: r.CreatedNs,
	}
	if r.User != nil {
		g.User = *r.User
	}
	if r.Group != nil {
		g.Group = *r.Group
	}
	return g
}

// warn logs a failure that must not fail the operation that hit it. Every
// use has the same shape: the work already succeeded and the bookkeeping
// after it is best-effort.
func (c *Core) warn(msg string, attrs ...any) {
	c.logger.Warn(msg, attrs...)
}

// errf wraps a sentinel with the context the caller needs to act on it.
func errf(wrap error, format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, wrap)...)
}
