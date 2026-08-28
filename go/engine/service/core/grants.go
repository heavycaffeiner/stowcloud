//go:build linux

package core

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Grant administration: thin wrappers over the store's grant aggregate, each
// followed by one evaluator reload.
//
// These exist because three call sites used to build grant SQL against a raw
// store handle and reload separately, or not at all. The store owns the
// statements, the wrapper owns the reload, so a grant write and the evaluator
// that serves it cannot drift apart at any call site.

// GrantSpec is one grant to persist. Exactly one of User and Group is set,
// which the store validates on the way in.
type GrantSpec struct {
	User    *int64
	Group   *int64
	Share   ShareID
	Subpath string
	Allow   acl.Perms
	Deny    acl.Perms
	Inherit bool
	Label   string
}

// GrantFilter narrows a listing. A zero field is not a filter.
type GrantFilter = state.GrantFilter

// Grant is one stored grant as a caller above the core sees it.
type Grant = state.GrantRow

// CreateGrant persists one grant and reloads the evaluator.
func (c *Core) CreateGrant(ctx context.Context, spec GrantSpec) (int64, error) {
	id, err := c.state.PersistGrant(ctx, state.GrantRow{
		User:    spec.User,
		Group:   spec.Group,
		Share:   int64(spec.Share),
		Subpath: spec.Subpath,
		Allow:   uint16(spec.Allow),
		Deny:    uint16(spec.Deny),
		Inherit: spec.Inherit,
		Label:   spec.Label,
	}, c.clk.Nanos())
	if err != nil {
		return 0, err
	}
	if rerr := c.ReloadGrants(ctx); rerr != nil {
		return 0, rerr
	}
	return id, nil
}

// ListGrants reads the stored grants a filter selects. It is a read, so no
// reload follows it.
func (c *Core) ListGrants(ctx context.Context, filter GrantFilter) ([]Grant, error) {
	return c.state.ListGrants(ctx, filter)
}

// UpdateGrant changes one grant's permissions and reloads the evaluator.
func (c *Core) UpdateGrant(
	ctx context.Context, id int64, allow, deny acl.Perms, inherit bool, label string,
) error {
	if err := c.state.UpdateGrant(ctx, id, uint16(allow), uint16(deny), inherit, label); err != nil {
		return err
	}
	return c.ReloadGrants(ctx)
}

// DeleteGrant removes one grant and reloads the evaluator.
//
// The reload is what makes a revocation take effect in the running process.
// Without it the row is gone and the evaluator keeps answering from the set
// it loaded at startup, so a revoked user keeps their access until a restart.
func (c *Core) DeleteGrant(ctx context.Context, id int64) error {
	if err := c.state.DeleteGrant(ctx, id); err != nil {
		return err
	}
	return c.ReloadGrants(ctx)
}
