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

// GrantFilter restricts a listing. Fields left at zero impose no restriction.
type GrantFilter = state.GrantFilter

// Grant is one stored grant as a caller above the core sees it.
type Grant = state.GrantRow

// CreateGrant persists one grant and reloads the evaluator.
//
// The stored row is returned, not just its id: the screen renders the grant it
// just created, and an id alone leaves it with a row whose permission lists
// are missing. Reading them back would be a second query for values this
// already holds.
func (c *Core) CreateGrant(ctx context.Context, spec GrantSpec) (Grant, error) {
	row := state.GrantRow{
		User:    spec.User,
		Group:   spec.Group,
		Share:   int64(spec.Share),
		Subpath: spec.Subpath,
		Allow:   uint16(spec.Allow),
		Deny:    uint16(spec.Deny),
		Inherit: spec.Inherit,
		Label:   spec.Label,
	}
	created := c.clk.Nanos()
	id, err := c.state.PersistGrant(ctx, row, created)
	if err != nil {
		return Grant{}, err
	}
	if rerr := c.ReloadGrants(ctx); rerr != nil {
		return Grant{}, rerr
	}
	row.ID = id
	row.CreatedNs = created
	return row, nil
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

// GrantEveryShare gives one account every permission over every registered
// share, which is what makes a fresh deployment usable.
//
// A share is reachable only through a grant and a first run has none, so
// without this the first administrator signs in to an empty interface with no
// way to give itself anything.
//
// Every permission, because this is the administrator: a first account that
// could read and not write would be a different dead end. Broken shares are
// included, since the grant outlives a disk being absent and the account
// should not have to care which shares happened to mount during setup.
//
// One reload at the end rather than one per share. The intermediate states are
// not ones any request should be answered from.
func (c *Core) GrantEveryShare(ctx context.Context, user int64) error {
	const all = acl.Read | acl.Write | acl.Create | acl.Delete |
		acl.Rename | acl.Move | acl.Share | acl.Download

	for _, def := range c.Shares() {
		_, err := c.state.PersistGrant(ctx, state.GrantRow{
			User:    &user,
			Share:   int64(def.ID),
			Allow:   uint16(all),
			Inherit: true,
			// The share's own name: the interface draws the label as the
			// folder, and an unlabeled grant falls back to a generated one.
			Label: def.Name,
		}, c.clk.Nanos())
		if err != nil {
			return err
		}
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
