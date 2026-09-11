//go:build linux

package core

import (
	"context"
	"errors"
	"math"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// FreeSpace is what the filesystem has left, which is a different question
// from what an account may use. Both are called quota; only this one is
// about the disk.
type FreeSpace struct {
	// Used is what the filesystem holds, root-reserved blocks included.
	Used uint64

	// Available is what an unprivileged writer can consume: f_bavail, never
	// f_bfree. The blocks in the difference are reserved for root and
	// reporting them invites a write that then fails.
	Available uint64

	// Total holds the filesystem's overall size.
	Total uint64
}

// FreeSpace reports the space at the resolved path's own filesystem.
//
// At the path, not at the share root: a share with an array mounted at one
// of its subdirectories holds two filesystems, and a client asking about the
// array's folder must be told the array's numbers.
func (c *Core) FreeSpace(ctx context.Context, r Resolved) (FreeSpace, error) {
	if err := r.Require(acl.Read); err != nil {
		return FreeSpace{}, err
	}
	s, err := r.root.Space(r.path)
	if err != nil {
		return FreeSpace{}, mapVFSErr(err)
	}
	return FreeSpace{Used: s.Used(), Available: s.Available, Total: s.Total}, nil
}

// QuotaSink is the per-user byte ledger. The store layer implements it; the
// core only consumes it, and a Core with none attached enforces no cap,
// which is a legitimate deployment rather than a degraded one.
//
// User ids cross as int64 because UserID is a core type and the core imports
// the store, not the other way round.
type QuotaSink interface {
	// Reserve books additional bytes against user if the cap allows it. ok
	// false with a nil error is the cap refusal; an error is the ledger
	// itself failing. It must be one guarded update, never a read followed
	// by a write, because concurrent uploads racing the same headroom is
	// the case the cap exists for.
	Reserve(ctx context.Context, user int64, additional uint64) (ok bool, err error)

	// Commit settles a reservation whose write landed. Reserve already
	// booked the bytes, so this is idempotent and exists to keep the
	// caller's intent legible at the call site.
	Commit(ctx context.Context, user int64, additional uint64) error

	// Release credits bytes back: a reservation whose write did not land,
	// or bytes a permanent delete freed. The direction is fixed, so the
	// argument is a non-negative magnitude.
	Release(ctx context.Context, user int64, delta int64) error
}

// AttachQuotaSink installs the ledger. One-shot: replacing a sink at runtime
// would orphan whatever the old one booked, and a second call is a startup
// wiring bug rather than a runtime condition.
func (c *Core) AttachQuotaSink(sink QuotaSink) error {
	if c.quota != nil {
		return errors.New("a quota sink is already attached")
	}
	c.quota = sink
	return nil
}

// chargeQuota settles the ledger after a filesystem change has committed.
//
// The sign is the direction: negative is bytes freed and credits through
// Release with the magnitude; positive is bytes grown and books through
// Reserve. Both are best-effort, because the disk change is already durable
// and failing the request over its bookkeeping would report an operation as
// failed that in fact happened. A refused booking undercounts the account,
// which never blocks a later legitimate write on drift it did not cause.
func (c *Core) chargeQuota(ctx context.Context, user UserID, delta int64) {
	if c.quota == nil || delta == 0 {
		return
	}
	if delta > 0 {
		ok, err := c.quota.Reserve(ctx, int64(user), uint64(delta))
		switch {
		case err != nil:
			c.warn("settling the quota ledger failed; the filesystem change has committed",
				"error", err)
		case !ok:
			c.warn("the quota ledger refused a charge; the filesystem change has committed",
				"user", int64(user), "bytes", delta)
		}
		return
	}
	if err := c.quota.Release(ctx, int64(user), magnitude(delta)); err != nil {
		c.warn("settling the quota ledger failed; the filesystem change has committed",
			"error", err)
	}
}

// magnitude is -delta for a negative delta, with the most negative int64
// clamped: negating it wraps back to itself, which would hand Release a
// negative it refuses.
func magnitude(delta int64) int64 {
	if delta == math.MinInt64 {
		return math.MaxInt64
	}
	return -delta
}

// CheckQuota tests whether an account's quota ledger can accommodate bytes.
func (c *Core) CheckQuota(ctx context.Context, user UserID, bytes uint64) error {
	if c.quota == nil || bytes == 0 {
		return nil
	}
	signed, err := num.Narrow[int64](bytes)
	if err != nil {
		return err
	}
	ok, err := c.quota.Reserve(ctx, int64(user), bytes)
	if err != nil {
		return err
	}
	if !ok {
		return ErrQuotaExceeded
	}
	if rerr := c.quota.Release(ctx, int64(user), signed); rerr != nil {
		c.warn("releasing quota reservation failed", "error", rerr)
	}
	return nil
}

// quotaReservation is a positive-growth booking held until the filesystem
// publication succeeds. Reserve books immediately, so a failed pre-commit
// operation must release the same bytes rather than trying to repair usage
// after the fact.
type quotaReservation struct {
	bytes  uint64
	active bool
}

// reserveQuota admits prospective growth before a filesystem change commits.
// A false reservation is the quota refusal, not a ledger failure.
func (c *Core) reserveQuota(ctx context.Context, user UserID, bytes uint64) (quotaReservation, error) {
	if c.quota == nil || bytes == 0 {
		return quotaReservation{}, nil
	}
	if _, err := num.Narrow[int64](bytes); err != nil {
		return quotaReservation{}, err
	}
	ok, err := c.quota.Reserve(ctx, int64(user), bytes)
	if err != nil {
		return quotaReservation{}, err
	}
	if !ok {
		return quotaReservation{}, ErrQuotaExceeded
	}
	return quotaReservation{bytes: bytes, active: true}, nil
}

// commitQuota closes a reservation after the corresponding publication. The
// state ledger's Commit is intentionally idempotent, and a bookkeeping failure
// cannot turn a durable filesystem change into a reported failure.
func (c *Core) commitQuota(ctx context.Context, user UserID, hold *quotaReservation) {
	if hold == nil || !hold.active || c.quota == nil {
		return
	}
	if err := c.quota.Commit(ctx, int64(user), hold.bytes); err != nil {
		c.warn("committing quota reservation failed; the filesystem change has committed",
			"error", err)
	}
	hold.active = false
}

// releaseQuota returns a pre-commit booking when publication did not happen.
func (c *Core) releaseQuota(ctx context.Context, user UserID, hold *quotaReservation) {
	if hold == nil || !hold.active || c.quota == nil {
		return
	}
	if rel, err := num.Narrow[int64](hold.bytes); err != nil {
		c.warn("releasing quota reservation failed; its size does not fit the ledger",
			"bytes", hold.bytes, "error", err)
	} else if rerr := c.quota.Release(ctx, int64(user), rel); rerr != nil {
		c.warn("releasing quota reservation failed", "error", rerr)
	}
	hold.active = false
}

// resizeQuota adjusts a held booking before publication when the staged
// result's actual size differs from the initial estimate.
func (c *Core) resizeQuota(
	ctx context.Context, user UserID, hold *quotaReservation, target uint64,
) error {
	if hold == nil || c.quota == nil {
		return nil
	}
	switch {
	case target > hold.bytes:
		additional := target - hold.bytes
		next, err := c.reserveQuota(ctx, user, additional)
		if err != nil {
			return err
		}
		hold.bytes += next.bytes
	case target < hold.bytes:
		credit := hold.bytes - target
		rel, err := num.Narrow[int64](credit)
		if err != nil {
			return err
		}
		if err := c.quota.Release(ctx, int64(user), rel); err != nil {
			return err
		}
		hold.bytes = target
	}
	hold.active = hold.bytes > 0
	return nil
}

// settleQuota completes a filesystem byte delta. Positive growth must already
// have a reservation; negative growth is a post-commit credit.
func (c *Core) settleQuota(
	ctx context.Context, user UserID, delta int64, hold *quotaReservation,
) {
	if delta > 0 {
		c.commitQuota(ctx, user, hold)
		return
	}
	if delta < 0 {
		c.chargeQuota(ctx, user, delta)
	}
}

// writeDurableQuota wraps the common durable-write boundary. The callback
// writes into VFS's private staging file, so its final stat is available before
// publication and quota admission can still refuse without touching the old
// destination.
func (c *Core) writeDurableQuota(
	ctx context.Context,
	r Resolved,
	mode vfs.DurableOpts,
	prior *vfs.Stat,
	write func(*vfs.File) error,
) (vfs.Durable, int64, error) {
	var oldSize uint64
	if prior != nil {
		oldSize = prior.Size
	}
	var (
		hold  quotaReservation
		delta int64
	)
	done, err := r.root.WriteDurable(r.path, mode, func(f *vfs.File) error {
		if err := write(f); err != nil {
			return err
		}
		st, err := f.Stat()
		if err != nil {
			return err
		}
		delta = deltaOf(st.Size, oldSize)
		if delta <= 0 {
			return nil
		}
		hold, err = c.reserveQuota(ctx, r.user, uint64(delta))
		return err
	})
	if err != nil {
		c.releaseQuota(ctx, r.user, &hold)
		return done, delta, err
	}
	c.settleQuota(ctx, r.user, delta, &hold)
	return done, delta, nil
}

// treeBytes measures file bytes beneath a path without using the aggregate
// cache. Control paths are deliberately supported here because trash entries
// live beneath one; the policy decides whether reserved children participate.
func treeBytes(root vfs.Root, p vfs.SafePath, policy vfs.ReservedPolicy) (uint64, error) {
	st, err := root.Stat(p)
	if err != nil {
		return 0, mapVFSErr(err)
	}
	if !st.Kind.IsDir() {
		return st.Size, nil
	}
	entries, err := root.ReadDir(p, policy)
	if err != nil {
		return 0, mapVFSErr(err)
	}
	var total uint64
	for _, e := range entries {
		child, jerr := joinTreeChild(p, e.Name)
		if jerr != nil {
			return 0, mapVFSErr(jerr)
		}
		size, serr := treeBytes(root, child, policy)
		if serr != nil {
			return 0, serr
		}
		if ^uint64(0)-total < size {
			return ^uint64(0), nil
		}
		total += size
	}
	return total, nil
}

// joinTreeChild preserves the validation distinction between a name read from
// an ordinary listing and a trusted control name read by a maintenance walk.
func joinTreeChild(parent vfs.SafePath, name string) (vfs.SafePath, error) {
	if vfs.IsReservedName(name) {
		return parent.JoinControl(name)
	}
	return parent.JoinExisting(name)
}
