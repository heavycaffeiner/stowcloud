//go:build linux

package dav

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	lock "github.com/heavycaffeiner/stowcloud/backend/internal/feature/dav/lock"
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/ident"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// StateLocks joins the durable lock table to the WebDAV protocol's lock types.
// The protocol uses opaque resource keys while the database records filesystem
// identities, so this translation belongs beside the protocol adapter.
type StateLocks struct {
	locks *lock.Locks
	log   *slog.Logger
}

// NewStateLocks wraps a state database.
func NewStateLocks(db *state.DB, clk clock.Clock, log *slog.Logger) *StateLocks {
	if log == nil {
		log = slog.Default()
	}
	return &StateLocks{locks: lock.New(db, clk), log: log}
}

// Guard answers whether a write may proceed against locks covering a path.
func (d *StateLocks) Guard(ctx context.Context, share uint32, path string, principal int64, submitted []string) error {
	return d.locks.Guard(ctx, share, path, principal, submitted)
}

// Take creates a lock.
func (d *StateLocks) Take(ctx context.Context, req LockRequest) (Lock, error) {
	id, ok := req.Key.(ident.Ident)
	if !ok {
		return Lock{}, fmt.Errorf("a lock key of type %T", req.Key)
	}
	depth := state.LockDepthZero
	if req.Infinite {
		depth = state.LockDepthInfinity
	}
	got, err := d.locks.Take(ctx, lock.Request{
		Ident: id, Path: req.Path, Principal: req.Principal, Owner: req.Owner,
		Depth: depth, Timeout: req.Timeout, Shared: req.Shared,
	})
	if err != nil {
		return Lock{}, protocolLockError(err)
	}
	return lockOf(got), nil
}

func protocolLockError(err error) error {
	switch {
	case errors.Is(err, lock.ErrLocked):
		return ErrLocked
	case errors.Is(err, lock.ErrNoSuchLock):
		return core.ErrNotFound
	default:
		return err
	}
}

// Refresh extends a lock its holder already has.
func (d *StateLocks) Refresh(ctx context.Context, token string, principal int64, timeout time.Duration) (Lock, error) {
	got, err := d.locks.Refresh(ctx, token, principal, timeout)
	if err != nil {
		return Lock{}, protocolLockError(err)
	}
	return lockOf(got), nil
}

// Release drops a lock.
func (d *StateLocks) Release(ctx context.Context, token string, principal int64) error {
	return protocolLockError(d.locks.Release(ctx, token, principal))
}

// At reports locks covering a path for lockdiscovery.
func (d *StateLocks) At(ctx context.Context, share uint32, path string) []Lock {
	got, err := d.locks.At(ctx, share, path)
	if err != nil {
		d.log.Warn("the lock table could not be read for lockdiscovery", "share", share, "path", path, "error", err)
		return nil
	}
	out := make([]Lock, 0, len(got))
	for _, l := range got {
		out = append(out, lockOf(l))
	}
	return out
}

// Tokens reports tokens covering a path for an If header.
func (d *StateLocks) Tokens(ctx context.Context, share uint32, path string) []string {
	got, err := d.locks.At(ctx, share, path)
	if err != nil {
		d.log.Warn("the lock table could not be read for an If header", "share", share, "path", path, "error", err)
		return nil
	}
	out := make([]string, 0, len(got))
	for _, l := range got {
		out = append(out, lock.TokenURN(l.Token))
	}
	return out
}

func lockOf(l lock.Active) Lock {
	return Lock{
		Token: lock.TokenURN(l.Token), Path: l.Path, Owner: l.Owner,
		TimeoutS: l.TimeoutS, Infinite: l.Depth == state.LockDepthInfinity,
		Exclusive: !l.Shared,
	}
}
