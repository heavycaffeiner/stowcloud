//go:build linux

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/davlock"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Joining the lock table to the WebDAV methods that use it.
//
// The protocol package describes a lock in its own terms and keys a resource
// with an opaque value. The service records one against a filesystem identity.
// This is the one tier that sees both, so the translation is here.

// DavLocks adapts the lock service to what the protocol expects.
type DavLocks struct {
	locks *davlock.Locks
	log   *slog.Logger
}

// NewDavLocks wraps the lock table.
func NewDavLocks(db *state.DB, clk clock.Clock, log *slog.Logger) *DavLocks {
	if log == nil {
		log = slog.Default()
	}
	return &DavLocks{locks: davlock.New(db, clk), log: log}
}

// Guard answers whether a write may proceed against the locks in the way.
func (d *DavLocks) Guard(
	ctx context.Context, share uint32, path string, principal int64, submitted []string,
) error {
	return d.locks.Guard(ctx, share, path, principal, submitted)
}

// Take creates a lock.
func (d *DavLocks) Take(ctx context.Context, req dav.LockRequest) (dav.Lock, error) {
	id, ok := req.Key.(ident.Ident)
	if !ok {
		return dav.Lock{}, fmt.Errorf("a lock key of type %T", req.Key)
	}

	depth := state.LockDepthZero
	if req.Infinite {
		depth = state.LockDepthInfinity
	}

	got, err := d.locks.Take(ctx, davlock.Request{
		Ident:     id,
		Path:      req.Path,
		Principal: req.Principal,
		Owner:     req.Owner,
		Depth:     depth,
		Timeout:   req.Timeout,
		Shared:    req.Shared,
	})
	if err != nil {
		return dav.Lock{}, protocolError(err)
	}
	return davLockOf(got), nil
}

// protocolError turns a lock service failure into the protocol's own.
//
// The two packages define their own sentinels on purpose: one states a fact
// about the domain and the other is how WebDAV answers it. Without the
// translation the status table does not recognise the service's error and
// answers 500, so a client contending for a lock is told the server broke
// rather than that somebody else holds it.
func protocolError(err error) error {
	switch {
	case errors.Is(err, davlock.ErrLocked):
		return dav.ErrLocked
	case errors.Is(err, davlock.ErrNoSuchLock):
		return core.ErrNotFound
	default:
		return err
	}
}

// Refresh extends a lock its holder already has.
func (d *DavLocks) Refresh(
	ctx context.Context, token string, principal int64, timeout time.Duration,
) (dav.Lock, error) {
	got, err := d.locks.Refresh(ctx, token, principal, timeout)
	if err != nil {
		return dav.Lock{}, protocolError(err)
	}
	return davLockOf(got), nil
}

// Release drops a lock.
func (d *DavLocks) Release(ctx context.Context, token string, principal int64) error {
	return protocolError(d.locks.Release(ctx, token, principal))
}

// At reports the locks covering a path, for lockdiscovery.
//
// A failure is logged and reported as no locks rather than returned. This runs
// inside a PROPFIND whose document is already going out, so there is no status
// left to change, and the alternative is a property that claims a lock the
// table could not confirm.
func (d *DavLocks) At(ctx context.Context, share uint32, path string) []dav.Lock {
	got, err := d.locks.At(ctx, share, path)
	if err != nil {
		d.log.Warn("the lock table could not be read for lockdiscovery",
			"share", share, "path", path, "error", err)
		return nil
	}
	out := make([]dav.Lock, 0, len(got))
	for _, l := range got {
		out = append(out, davLockOf(l))
	}
	return out
}

// Tokens reports the tokens covering a path, for an If header naming one.
func (d *DavLocks) Tokens(ctx context.Context, share uint32, path string) []string {
	got, err := d.locks.At(ctx, share, path)
	if err != nil {
		// Reported as none rather than as an error. A token the table could
		// not confirm must not satisfy a condition asserting it.
		d.log.Warn("the lock table could not be read for an If header",
			"share", share, "path", path, "error", err)
		return nil
	}
	out := make([]string, 0, len(got))
	for _, l := range got {
		out = append(out, davlock.TokenURN(l.Token))
	}
	return out
}

// davLockOf projects a live lock into what the protocol renders.
//
// The token carries its URN form, because that is what a client submits back
// and what lockdiscovery has to print.
func davLockOf(l davlock.Active) dav.Lock {
	return dav.Lock{
		Token:     davlock.TokenURN(l.Token),
		Path:      l.Path,
		Owner:     l.Owner,
		TimeoutS:  l.TimeoutS,
		Infinite:  l.Depth == state.LockDepthInfinity,
		Exclusive: !l.Shared,
	}
}
