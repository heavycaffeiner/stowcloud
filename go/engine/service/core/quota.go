//go:build linux

package core

import (
	"context"
	"errors"
	"math"

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
	ok, err := c.quota.Reserve(ctx, int64(user), bytes)
	if err != nil {
		return err
	}
	if !ok {
		return ErrQuotaExceeded
	}
	if rel, nerr := num.Narrow[int64](bytes); nerr == nil {
		if rerr := c.quota.Release(ctx, int64(user), rel); rerr != nil {
			c.warn("releasing quota reservation failed", "error", rerr)
		}
	}
	return nil
}
