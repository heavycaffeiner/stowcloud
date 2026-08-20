//go:build linux

package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The admin-facing share registry: create, update and delete a share, and
// apply a persisted definition or override at startup. Admin-created shares
// and edits to config-defined shares are durable, because rebuilding the cache
// cannot recreate either one.

const dynamicShareIDBase = 1_000_000

// ConfiguredShare is how a config-defined share reaches RegisterShare:
// the id comes from the config's array position and is read-only from the
// admin API.
func (c *Core) ConfiguredShare(ctx context.Context, def ShareDef) error {
	// Apply any persisted name/path and trash overrides on top of what the
	// config declared, so an admin's edit survives a restart. This is the one
	// place a config-defined share's definition is adjusted at startup.
	name, host, err := c.appliedIdentity(ctx, int64(def.ID))
	if err != nil {
		return err
	}
	trash, terr := c.appliedTrash(ctx, int64(def.ID))
	if terr != nil {
		return terr
	}
	return c.RegisterShare(ctx, ShareDef{
		ID:               def.ID,
		Name:             name,
		Host:             host,
		Policy:           def.Policy,
		TrashEnabled:     trash,
		SharedExternally: def.SharedExternally,
	})
}

// appliedIdentity is the override or the original, the latter for the config
// path that has not been edited.
func (c *Core) appliedIdentity(ctx context.Context, id int64) (string, string, error) {
	o, ok, err := c.state.IdentityOverrideFor(ctx, id)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", nil
	}
	return o.Name, o.Host, nil
}

// appliedTrash is the persisted toggle, default off.
func (c *Core) appliedTrash(ctx context.Context, id int64) (bool, error) {
	on, _, err := c.state.TrashOverrideFor(ctx, id)
	return on, err
}

// CreateShare registers a validated host directory and returns the externally
// visible id stored with it. The id is dynamicShareIDBase plus the durable row
// id, which is what keeps a dynamic share out of the config-derived range.
func (c *Core) CreateShare(ctx context.Context, spec ShareSpec) (Share, error) {
	def, err := c.shareDefByName(spec.Name)
	if err != nil {
		return Share{}, err
	}
	if def != nil {
		return Share{}, errf(ErrConflict, "a share named %q already exists", spec.Name)
	}

	rowid, err := c.state.InsertShare(ctx, spec.Name, spec.Host, c.clk.Nanos())
	if err != nil {
		return Share{}, err
	}
	// The dynamic-share base is added to the fresh rowid, which is a small
	// positive number; a rowid that overflows the id space is corruption.
	// The id is uint32, so the narrow check must prove the uint32 range, not
	// just the uint64 one, or a rowid near the top of the space truncates.
	combined, nerr := num.Narrow[uint32](dynamicShareIDBase + rowid)
	if nerr != nil {
		_ = c.state.DeleteShare(ctx, rowid) //nolint:errcheck // the overflow is the answer; the rollback is best-effort.
		return Share{}, fmt.Errorf("a share row id overflowed the id space: %w", nerr)
	}
	id := ShareID(combined)
	trash, _, terr := c.state.TrashOverrideFor(ctx, int64(id))
	if terr != nil {
		_ = c.state.DeleteShare(ctx, rowid) //nolint:errcheck // the override error is the answer; the rollback is best-effort.
		return Share{}, terr
	}
	created := Share{
		ID:           id,
		Name:         spec.Name,
		Host:         spec.Host,
		Policy:       vfs.DefaultSharePolicy(),
		TrashEnabled: trash,
	}
	if err := c.RegisterShare(ctx, created); err != nil {
		// The durability write committed; a registration that then failed
		// leaves a row with no live share, so it is rolled back like the
		// reference does.
		_ = c.state.DeleteShare(ctx, rowid) //nolint:errcheck // the registration failure is the answer; the rollback is best-effort.
		return Share{}, fmt.Errorf("share rejected: %w", err)
	}
	return created, nil
}

// UpdateShare persists edits to a dynamic share, or the allowed overrides for
// a config-defined share. Only a dynamic share's name and host edit owns a
// row; a config-defined one goes to the override table that registration reads
// back.
func (c *Core) UpdateShare(ctx context.Context, id ShareID, patch SharePatch) (Share, error) {
	existing, ok := c.Share(id)
	if !ok {
		return Share{}, ErrNotFound
	}
	name := existing.Name
	if patch.Name != nil {
		name = *patch.Name
	}
	host := existing.Host
	if patch.Host != nil {
		host = *patch.Host
	}
	trashEnabled := existing.TrashEnabled
	if patch.TrashEnabled != nil {
		trashEnabled = *patch.TrashEnabled
	}

	if id < dynamicShareIDBase {
		// Config-defined: write the identity override and the trash override.
		if patch.Name != nil || patch.Host != nil {
			if err := c.state.SetIdentityOverride(ctx, int64(id), name, host); err != nil {
				return Share{}, err
			}
		}
		if patch.TrashEnabled != nil {
			if err := c.state.SetTrashOverride(ctx, int64(id), *patch.TrashEnabled); err != nil {
				return Share{}, err
			}
		}
	} else {
		rowid := int64(id) - dynamicShareIDBase
		if patch.Name != nil || patch.Host != nil {
			if err := c.state.UpdateShare(ctx, rowid, name, host); err != nil {
				return Share{}, err
			}
		}
		if patch.TrashEnabled != nil {
			if err := c.state.SetTrashOverride(ctx, int64(id), *patch.TrashEnabled); err != nil {
				return Share{}, err
			}
		}
	}

	// Re-register under the same id; an in-flight request holding the old
	// *vfs.ShareRoot finishes against it, and every request after this sees the
	// new one.
	def := Share{
		ID:               id,
		Name:             name,
		Host:             host,
		Policy:           existing.Policy,
		TrashEnabled:     trashEnabled,
		SharedExternally: existing.SharedExternally,
	}
	if err := c.RegisterShare(ctx, def); err != nil {
		return Share{}, err
	}
	return def, nil
}

// DeleteShare removes an admin-created share. A config-defined share is refused:
// nothing can stop the config from declaring it on the next restart.
func (c *Core) DeleteShare(ctx context.Context, id ShareID) error {
	if id < dynamicShareIDBase {
		return errf(ErrDenied, "a config-defined share cannot be deleted; edit the config and restart")
	}
	if _, ok := c.Share(id); !ok {
		return ErrNotFound
	}
	// Delete the durable row and the live root. Grants naming it are the
	// admin store's cascade; a dangling grant is default-deny anyway.
	return c.state.DeleteShare(ctx, int64(id)-dynamicShareIDBase)
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

var _ = errors.Is

// ReloadPersistedShares re-registers every admin-created share at startup,
// computing the same combined id CreateShare minted. A restart must land on
// the same ids the running process used, because the cache, the grants and
// the links all reference them.
func (c *Core) ReloadPersistedShares(ctx context.Context) (rejected []RejectedShare, err error) {
	rows, err := c.state.ListShares(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		combined, nerr := num.Narrow[uint32](dynamicShareIDBase + row.ID)
		if nerr != nil {
			return nil, fmt.Errorf("a persisted share row id overflowed the id space: %w", nerr)
		}
		def := ShareDef{
			ID:     ShareID(combined),
			Name:   row.Name,
			Host:   row.Host,
			Policy: vfs.DefaultSharePolicy(),
		}
		if rerr := c.RegisterShare(ctx, def); rerr != nil {
			// One share this server cannot serve is not an outage of every
			// other share. It is left unregistered and reported, so the
			// deployment starts, the rest of it works, and the health surface
			// says which one is missing and why.
			rejected = append(rejected, RejectedShare{
				Name: row.Name, Kind: rejectionKind(rerr), Err: rerr,
			})
			c.logger.Error("a share was refused and is not being served",
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

// rejectionKind reduces a registration failure to the token the health surface
// carries.
func rejectionKind(err error) string {
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
