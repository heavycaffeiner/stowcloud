//go:build linux

package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The share registry: create, update and delete a share, and register the
// persisted ones at startup. Every share is one of these; there is no config
// file declaring any, so there is one kind and one place it lives.
//
// The rows are durable because rebuilding the cache cannot recreate a folder
// somebody said to serve.

// dynamicShareIDBase keeps a share's external id away from the low numbers,
// which is not decoration: a deployment that predates the single registry
// minted ids in this range, and the grants, links and cache rows that
// reference them are still on disk. The offset is what keeps those references
// resolving to the same share.
const dynamicShareIDBase = 1_000_000

// shareIDOf is the external id for a stored row.
func shareIDOf(rowid int64) (ShareID, error) {
	// The base is added to the rowid, which is a small positive number; one
	// that overflows the id space is corruption. The id is uint32, so the
	// check has to prove that range and not merely uint64.
	combined, err := num.Narrow[uint32](dynamicShareIDBase + rowid)
	if err != nil {
		return 0, fmt.Errorf("a share row id overflowed the id space: %w", err)
	}
	return ShareID(combined), nil
}

// rowIDOf is the inverse, for a share this process registered.
func rowIDOf(id ShareID) int64 { return int64(id) - dynamicShareIDBase }

// CreateShare registers a host directory and returns the externally visible id
// stored with it.
func (c *Core) CreateShare(ctx context.Context, spec ShareSpec) (Share, error) {
	def, err := c.shareDefByName(spec.Name)
	if err != nil {
		return Share{}, err
	}
	if def != nil {
		return Share{}, errf(ErrConflict, "a share named %q already exists", spec.Name)
	}

	row := state.ShareRow{
		Name:          spec.Name,
		Host:          spec.Host,
		SymlinkPolicy: vfs.SymlinkDeny.String(),
	}
	rowid, err := c.state.InsertShare(ctx, row, c.clk.Nanos())
	if err != nil {
		return Share{}, err
	}
	id, ierr := shareIDOf(rowid)
	if ierr != nil {
		_ = c.state.DeleteShare(ctx, rowid) //nolint:errcheck // the overflow is the answer; the rollback is best-effort.
		return Share{}, ierr
	}
	created := Share{
		ID:     id,
		Name:   spec.Name,
		Host:   spec.Host,
		Policy: vfs.DefaultSharePolicy(),
	}
	if err := c.RegisterShare(ctx, created); err != nil {
		// The durability write committed; a registration that then failed
		// leaves a row with no live share, so it is rolled back.
		_ = c.state.DeleteShare(ctx, rowid) //nolint:errcheck // the registration failure is the answer; the rollback is best-effort.
		return Share{}, fmt.Errorf("share rejected: %w", err)
	}
	return created, nil
}

// UpdateShare persists edits to a share and re-registers it.
func (c *Core) UpdateShare(ctx context.Context, id ShareID, patch SharePatch) (Share, error) {
	existing, ok := c.Share(id)
	if !ok {
		return Share{}, ErrNotFound
	}
	def := existing
	if patch.Name != nil {
		def.Name = *patch.Name
	}
	if patch.Host != nil {
		def.Host = *patch.Host
	}
	if patch.TrashEnabled != nil {
		def.TrashEnabled = *patch.TrashEnabled
	}

	if err := c.state.UpdateShare(ctx, rowIDOf(id), state.ShareRow{
		Name:             def.Name,
		Host:             def.Host,
		SharedExternally: def.SharedExternally,
		TrashEnabled:     def.TrashEnabled,
		SymlinkPolicy:    def.Policy.Symlink.String(),
	}); err != nil {
		return Share{}, err
	}

	// Re-registered under the same id; an in-flight request holding the old
	// *vfs.ShareRoot finishes against it, and every request after this sees
	// the new one.
	if err := c.RegisterShare(ctx, def); err != nil {
		// The row is written and the new path will not open. The share stays
		// listed, now broken against the path that was just saved: dropping it
		// would hide the edit that caused this, and refusing the write outright
		// would make a repointed path unfixable while the old one is also gone.
		c.RegisterBroken(def, err)
		return Share{}, err
	}
	return def, nil
}

// RetryShare re-opens a broken share's root, for the disk that came back.
//
// It is the same registration the startup path runs, so a path that returned
// on a filesystem this server will not serve is still refused, and refused
// here where somebody is watching rather than per request.
func (c *Core) RetryShare(ctx context.Context, id ShareID) (Share, error) {
	def, ok := c.Share(id)
	if !ok {
		return Share{}, ErrNotFound
	}
	if err := c.RegisterShare(ctx, def); err != nil {
		c.RegisterBroken(def, err)
		return Share{}, err
	}
	def.BrokenReason = ""
	return def, nil
}

// DeleteShare removes a share and stops serving it.
func (c *Core) DeleteShare(ctx context.Context, id ShareID) error {
	if _, ok := c.Share(id); !ok {
		return ErrNotFound
	}
	// The durable row and the live root. Grants naming it are the admin
	// store's cascade; a dangling grant is default-deny anyway.
	if err := c.state.DeleteShare(ctx, rowIDOf(id)); err != nil {
		return err
	}
	c.UnregisterShare(id)
	return nil
}

// shareDefByName is the name-collision check before a create.
func (c *Core) shareDefByName(name string) (*ShareDef, error) {
	defs := c.Shares()
	for i := range defs {
		if defs[i].Name == name {
			return &defs[i], nil
		}
	}
	return nil, nil
}

// ReloadPersistedShares registers every stored share at startup, computing the
// same ids CreateShare minted. A restart must land on the same ids the running
// process used, because the cache, the grants and the links all reference them.
func (c *Core) ReloadPersistedShares(ctx context.Context) (rejected []RejectedShare, err error) {
	rows, err := c.state.ListShares(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id, ierr := shareIDOf(row.ID)
		if ierr != nil {
			return nil, ierr
		}
		policy := vfs.DefaultSharePolicy()
		// A stored policy this build does not have is the restrictive one with
		// a line, not a refused start: the alternative is a share that follows
		// links because nobody could read the word saying it should not.
		if row.SymlinkPolicy != "" {
			p, perr := vfs.ParseSymlinkPolicy(row.SymlinkPolicy)
			if perr != nil {
				c.logger.Error("a share names a symlink policy this build does not have; using the restrictive one",
					"share", row.Name, "policy", row.SymlinkPolicy)
			} else {
				policy.Symlink = p
			}
		}
		def := ShareDef{
			ID:               id,
			Name:             row.Name,
			Host:             row.Host,
			Policy:           policy,
			SharedExternally: row.SharedExternally,
			TrashEnabled:     row.TrashEnabled,
		}
		if rerr := c.RegisterShare(ctx, def); rerr != nil {
			// One share this server cannot serve is not an outage of every
			// other share. The deployment starts and the rest of it works.
			//
			// It is registered broken rather than left out. Left out, it was
			// absent from the admin list and from every user's root list, so a
			// drive that did not come back looked exactly like a share
			// somebody had deleted, and the only trace was a line on the
			// health endpoint.
			c.RegisterBroken(def, rerr)
			rejected = append(rejected, RejectedShare{
				Name: row.Name, Kind: RejectionKind(rerr), Err: rerr,
			})
			c.logger.Error("a share is registered but cannot be served",
				"share", row.Name, "error", rerr)
			continue
		}
	}
	return rejected, nil
}

// RejectedShare is a share that could not be registered, named so the health
// surface can say which one rather than only that one is missing.
type RejectedShare struct {
	Name string
	// Kind is a token for the health surface: the filesystem that was refused,
	// or a word for the class of failure. The health surface carries kinds
	// rather than sentences, and the sentence is in Err for the log.
	Kind string
	Err  error
}

// RejectionKind reduces a registration failure to the token the health surface
// carries. Exported because the assembly registers shares too.
func RejectionKind(err error) string {
	var adm *vfs.AdmissionError
	if errors.As(err, &adm) {
		return adm.Type.String()
	}
	if errors.Is(err, vfs.ErrNotFound) {
		return "missing"
	}
	if errors.Is(err, vfs.ErrDenied) {
		return "unreadable"
	}
	return "unavailable"
}
