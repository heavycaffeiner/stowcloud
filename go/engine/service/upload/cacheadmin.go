//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// The administrative surface over the cache: whether there is one, whether it
// is on, and turning it on or off.

// cacheEnabled reports whether new sessions route through the cache.
func (e *Engine) cacheEnabled() bool {
	return e.cache != nil && e.cache.enabled.Load()
}

// CacheEnabled is the administrator-facing read of the same switch.
func (e *Engine) CacheEnabled() bool { return e.cacheEnabled() }

// CacheAvailable reports whether this deployment has a spool at all.
//
// A deployment with no data directory has none, and the screen has to say so
// rather than offering a switch that does nothing.
func (e *Engine) CacheAvailable() bool { return e.cache != nil }

// SetCacheEnabled stores the switch and applies it immediately.
//
// The switch is consulted at session creation and never again, so toggling it
// affects only future sessions. A session already in flight has its bytes in one
// location or the other, and no setting relocates them.
//
// The probe precedes the write, so a spool unable to accept a file is rejected
// here with an administrator present rather than at the first upload following
// the next restart.
func (e *Engine) SetCacheEnabled(ctx context.Context, on bool) error {
	if e.cache == nil {
		return ErrNoCache
	}
	if on {
		if err := e.cache.probe(); err != nil {
			return err
		}
	}
	if err := e.state.WriteUploadCacheEnabled(ctx, on); err != nil {
		return err
	}
	e.cache.setEnabled(on)
	return nil
}

// probe creates and deletes a single file, the only reliable way to determine
// whether the spool accepts writes. A stat reports what the metadata asserts,
// and both a read-only mount and a directory owned by someone else satisfy
// it.
func (c *cacheSpool) probe() error {
	name, err := NewSessionID()
	if err != nil {
		return err
	}
	p, jerr := vfs.RootPath().JoinControl(".scpart-probe-" + name.String())
	if jerr != nil {
		return jerr
	}
	f, cerr := c.root.CreatePart(p)
	if cerr != nil {
		return fmt.Errorf("%w: the upload cache spool is not writable: %w", ErrBadRequest, cerr)
	}
	closeErr := f.Close()
	if uerr := c.root.Unlink(p); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		return fmt.Errorf("%w: the upload cache spool is not writable: %w", ErrBadRequest, uerr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: the upload cache spool is not writable: %w", ErrBadRequest, closeErr)
	}
	return nil
}

// setCacheBoundsForTest overrides the budget and the per-step copy bound.
//
// One bound is a share of a real volume's free space and the other is tens of
// megabytes, so proving behavior at either otherwise means moving that much
// data per test. Production never calls this.
func (e *Engine) setCacheBoundsForTest(budget, step int64) {
	if e.cache == nil {
		return
	}
	e.cache.limit.Store(budget)
	e.cache.step.Store(step)
}

// cacheUsedForTest is what the spool believes it holds.
func (e *Engine) cacheUsedForTest() int64 {
	if e.cache == nil {
		return 0
	}
	return e.cache.used.Load()
}
