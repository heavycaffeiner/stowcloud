//go:build linux

package core

import (
	"context"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Everything that touches the durable share rows or serves the admin API.
// The registry beside this file owns live state; this file owns the rows and
// the vocabulary, so neither has to read the other end to end.

// dynamicShareIDBase offsets a rowid into a share id.
//
// The offset must be preserved exactly. Deployments that predate the single
// registry minted ids in this range, and the grants, share links and cache
// rows referencing those ids are still on disk: any other mapping makes a
// restart resolve old references to the wrong share or to nothing.
//
// It also reserves the homes share id (999_999, in home.go), which sits
// below the base. Rowids are positive, so dynamic ids start at 1_000_001 and
// no stored share can ever mint it.
const dynamicShareIDBase = 1_000_000

// shareIDOf maps a rowid to its external id, refusing an overflow rather
// than truncating: an id that does not fit is corruption, and a truncated
// one would collide with a real share.
func shareIDOf(rowid int64) (ShareID, error) {
	narrowed, err := num.Narrow[uint32](rowid + dynamicShareIDBase)
	if err != nil {
		return 0, fmt.Errorf("share rowid %d does not fit a share id: %w", rowid, err)
	}
	return ShareID(narrowed), nil
}

// rowIDOf is the inverse.
func rowIDOf(id ShareID) int64 { return int64(id) - dynamicShareIDBase }

// RejectedShare is one share a reload could not register.
type RejectedShare struct {
	Name string
	// Kind is the health surface's token; the sentence is in Err.
	Kind string
	Err  error
}

// rowOf projects a definition onto the durable row shape.
func rowOf(def ShareDef) state.ShareRow {
	return state.ShareRow{
		Name:             def.Name,
		Host:             def.Host,
		SharedExternally: def.SharedExternally,
		TrashEnabled:     def.TrashEnabled,
		SymlinkPolicy:    def.Policy.Symlink.String(),
	}
}

// CreateShare mints a share, both durably and in the live registry.
func (c *Core) CreateShare(ctx context.Context, spec ShareSpec) (Share, error) {
	// A linear scan, because the registry is small and already sorted.
	for _, existing := range c.Shares() {
		if existing.Name == spec.Name {
			return Share{}, errf(ErrConflict, "a share named %q already exists", spec.Name)
		}
	}

	policy := vfs.DefaultSharePolicy()
	row := state.ShareRow{
		Name: spec.Name, Host: spec.Host,
		SymlinkPolicy: policy.Symlink.String(),
	}
	rowid, err := c.state.InsertShare(ctx, row, c.clk.Nanos())
	if err != nil {
		return Share{}, err
	}

	id, err := shareIDOf(rowid)
	if err != nil {
		// The durable write committed and cannot be honoured, so the row is
		// rolled back rather than left dangling.
		c.rollbackShareRow(ctx, rowid, 0)
		return Share{}, err
	}

	def := ShareDef{ID: id, Name: spec.Name, Host: spec.Host, Policy: policy}
	if rerr := c.RegisterShare(ctx, def); rerr != nil {
		c.rollbackShareRow(ctx, rowid, int64(id))
		// Named as a share problem rather than passed through raw. A refused
		// registration is an admission verdict, which nothing above this
		// classifies, so it reached the screen as a bare 500: the folder was
		// not created and the reason went only to this process's memory.
		c.warn("a share was refused and its row rolled back",
			"name", spec.Name, "error", rerr)
		return Share{}, &ShareBrokenError{Share: spec.Name, Reason: RejectionKind(rerr)}
	}
	return def, nil
}

// rollbackShareRow undoes a row whose share could not be brought up. Its own
// failure is logged rather than returned, because the caller is already
// being told the first failure.
func (c *Core) rollbackShareRow(ctx context.Context, rowid, shareID int64) {
	if err := c.state.DeleteShare(ctx, rowid, shareID); err != nil {
		c.warn("rolling back a share row failed; it has no live share",
			"rowid", rowid, "error", err)
	}
}

// UpdateShare applies a patch and re-registers under the same id.
//
// An in-flight request holding the old root finishes against it; every
// request after sees the new one.
func (c *Core) UpdateShare(ctx context.Context, id ShareID, patch SharePatch) (Share, error) {
	def, ok := c.Share(id)
	if !ok {
		return Share{}, ErrNotFound
	}
	if patch.Name != nil {
		def.Name = *patch.Name
	}
	if patch.Host != nil {
		def.Host = *patch.Host
	}
	if patch.TrashEnabled != nil {
		def.TrashEnabled = *patch.TrashEnabled
	}

	if err := c.state.UpdateShare(ctx, rowIDOf(id), rowOf(def)); err != nil {
		return Share{}, err
	}
	if err := c.RegisterShare(ctx, def); err != nil {
		// The row stays written. Dropping the entry would hide the edit that
		// caused the failure, and refusing the write would make a repointed
		// path unfixable when the old path is also gone.
		c.RegisterBroken(def, err)
		c.warn("a share edit left it unservable", "name", def.Name, "error", err)
		return Share{}, &ShareBrokenError{Share: def.Name, Reason: RejectionKind(err)}
	}
	def.BrokenReason = ""
	return def, nil
}

// RetryShare re-runs the full registration a fixed path needs, so the
// admission gate runs again: a path that came back on a filesystem this
// server refuses stays broken.
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

// DeleteShare removes the durable row and then the live entry.
//
// The store takes both ids: the row to delete and the external id whose
// grants it cascades in the same transaction, so a dangling grant can no
// longer outlive its share.
func (c *Core) DeleteShare(ctx context.Context, id ShareID) error {
	if _, ok := c.Share(id); !ok {
		return ErrNotFound
	}
	if err := c.state.DeleteShare(ctx, rowIDOf(id), int64(id)); err != nil {
		return err
	}
	c.UnregisterShare(id)
	// The cascade removed grant rows, so the evaluator has to stop serving
	// them in this process rather than at the next restart.
	return c.ReloadGrants(ctx)
}

// ReloadPersistedShares registers every stored share, computing the same id
// CreateShare minted.
//
// A restart must land on the same ids the running process used, because the
// cache, the grants and the links all reference them.
func (c *Core) ReloadPersistedShares(ctx context.Context) ([]RejectedShare, error) {
	rows, err := c.state.ListShares(ctx)
	if err != nil {
		return nil, err
	}

	var rejected []RejectedShare
	for _, row := range rows {
		id, ierr := shareIDOf(row.ID)
		if ierr != nil {
			// Corruption, not a share to skip.
			return nil, ierr
		}

		policy := vfs.DefaultSharePolicy()
		if parsed, perr := vfs.ParseSymlinkPolicy(row.SymlinkPolicy); perr != nil {
			// The restrictive default, never a refused start: the
			// alternative is a share that follows links because nobody could
			// read the word saying it should not.
			c.logger.Error("a share's symlink policy is unreadable; falling back to the strictest",
				"share", row.Name, "stored", row.SymlinkPolicy, "error", perr)
		} else {
			policy.Symlink = parsed
		}

		def := ShareDef{
			ID: id, Name: row.Name, Host: row.Host, Policy: policy,
			TrashEnabled: row.TrashEnabled, SharedExternally: row.SharedExternally,
		}
		if rerr := c.RegisterShare(ctx, def); rerr != nil {
			// A single unservable share does not take down all the others.
			c.RegisterBroken(def, rerr)
			rejected = append(rejected, RejectedShare{
				Name: row.Name, Kind: RejectionKind(rerr), Err: rerr,
			})
			c.logger.Error("a share would not register and is marked broken",
				"share", row.Name, "error", rerr)
		}
	}
	return rejected, nil
}

// ScanSource is one share as the search walker consumes it.
//
// The shape is core-owned and the search service adapts it, rather than the
// core importing search: search consumes the core's shares, so the
// dependency points that way.
type ScanSource struct {
	Share ShareID
	Root  *vfs.ShareRoot
	Base  vfs.SafePath
	// Allow reports whether the caller may see a path. Nil means everything,
	// which is the administrator-scoped form.
	Allow func(p vfs.SafePath, isDir bool) bool
}

// ScanSources is every registered share, administrator-scoped by design: the
// index covers every share, so sizing it against one account's view would
// report a number the built index does not match. The caller checks who is
// asking.
//
// A broken entry's nil root passes through; the search walker owns skipping
// a source it cannot open.
func (c *Core) ScanSources() []ScanSource {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()

	out := make([]ScanSource, 0, len(c.shares))
	for id, e := range c.shares {
		out = append(out, ScanSource{Share: id, Root: e.root, Base: vfs.RootPath()})
	}
	return out
}

// UserScanSources does the same, limited to what one account can read.
//
// Checking happens per entry rather than per share, because a grant may begin
// partway down a tree. A share-level answer would either conceal a readable
// subtree or include an unreadable one.
func (c *Core) UserScanSources(user UserID) []ScanSource {
	out := c.ScanSources()
	for i := range out {
		share := int64(out[i].Share)
		out[i].Allow = func(p vfs.SafePath, _ bool) bool {
			at := acl.Vpath{Share: share, Path: aclPath(p)}
			return c.acl.Evaluate(int64(user), at, acl.Read).Allowed
		}
	}
	return out
}

// ShareLabel is the label this account navigates a share under, empty when
// the account cannot see it.
//
// A caller rendering a search hit has already checked that it can; an empty
// answer there means the grant went away between the search and the render.
func (c *Core) ShareLabel(user UserID, share ShareID) string {
	for _, r := range c.labelledRoots(user) {
		narrowed, err := num.Narrow[uint32](r.Share)
		if err != nil {
			continue
		}
		if ShareID(narrowed) == share {
			return r.Label
		}
	}
	return ""
}
