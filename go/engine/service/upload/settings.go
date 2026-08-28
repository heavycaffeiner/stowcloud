//go:build linux

package upload

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Settings holds the live chunk floor and default, the two values that can
// change after the engine is built without requiring a restart.
//
// Implemented with plain atomics rather than a mutex. Every reader wants the
// current pair, nothing coordinates the two fields against each other, and a
// reader observing one write's floor alongside another's default is
// indistinguishable from two sessions created moments apart.
type Settings struct {
	minBytes     atomic.Uint64
	defaultBytes atomic.Uint64

	// override records whether a row exists at all, kept beside the values
	// rather than derived from them. Falling back collapses the two: once it
	// has run, "an administrator stored this" and "it fell back" are the same
	// pair of integers, and the settings screen has to report which.
	override atomic.Bool
}

// loadSettings seeds from the configuration and lets a persisted override
// win, so a restart does not lose an administrator's write.
//
// The ladder, in order: the compiled-in floor beats the persisted override,
// which beats the configuration seed.
func loadSettings(ctx context.Context, st *state.DB, cfgMin, cfgDefault uint64) (*Settings, error) {
	if cfgMin == 0 {
		cfgMin = limits.UploadChunkMinDefault
	}
	if cfgDefault == 0 {
		cfgDefault = limits.UploadChunkSizeDefault
	}
	s := &Settings{}
	s.store(cfgMin, cfgDefault)

	stored, err := st.ReadChunkSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !stored.Override {
		return s, nil
	}
	minBytes, minErr := num.Narrow[uint64](stored.Min)
	defBytes, defErr := num.Narrow[uint64](stored.Default)
	if minErr != nil || defErr != nil {
		// An unreadable stored pair is no reason to refuse startup. The
		// configuration's values serve as a working fallback and the override is
		// reported absent, which is effectively what it has become.
		return s, nil
	}
	s.store(minBytes, defBytes)
	s.override.Store(true)
	return s, nil
}

// store clamps both values to the floor on entry, so no path can leave a live
// value beneath the compiled-in minimum.
func (s *Settings) store(minBytes, defaultBytes uint64) {
	if minBytes < limits.UploadChunkFloor {
		minBytes = limits.UploadChunkFloor
	}
	if defaultBytes < minBytes {
		defaultBytes = minBytes
	}
	s.minBytes.Store(minBytes)
	s.defaultBytes.Store(defaultBytes)
}

// Min gives the current floor a mid-stream chunk is measured against.
func (s *Settings) Min() uint64 { return s.minBytes.Load() }

// Default gives the currently advertised chunk size. It recommends rather than
// requires: declining to impose a ceiling does not remove middleboxes, it just
// means this server is not the one doing the rejecting.
func (s *Settings) Default() uint64 { return s.defaultBytes.Load() }

// Snapshot returns both values for a caller that keeps them together.
func (s *Settings) Snapshot() (minBytes, defaultBytes uint64) {
	return s.minBytes.Load(), s.defaultBytes.Load()
}

// Overridden reports whether an administrator has ever written these, as
// opposed to them coming from the configuration. It is the only thing that
// can answer that for the settings screen.
func (s *Settings) Overridden() bool { return s.override.Load() }

// ApplySettings persists an administrator's chunk bounds and activates them.
//
// This is the sole write path. A second one previously existed doing the same
// work, and only this one had any caller outside a test.
//
// The sequencing matters. Neither a validation failure nor a failed disk write
// can leave the in-memory pair diverging from what is on disk, which is what a
// restart would subsequently read. A nil value leaves that half untouched, so a
// screen editing one field does not quietly reset the other.
//
// Sessions in flight are unaffected: both numbers are captured once at creation,
// so raising the floor cannot retroactively reject a chunk that was legal when
// the session began.
func (e *Engine) ApplySettings(ctx context.Context, minBytes, defaultBytes *uint64) error {
	curMin, curDefault := e.settings.Snapshot()
	newMin, newDefault := curMin, curDefault
	if minBytes != nil {
		newMin = *minBytes
	}
	if defaultBytes != nil {
		newDefault = *defaultBytes
	}
	if newMin < limits.UploadChunkFloor {
		return fmt.Errorf("%w: the minimum chunk is below the compiled-in floor of %d bytes",
			ErrBadRequest, limits.UploadChunkFloor)
	}
	// A default beneath the minimum yields a configuration where every chunk the
	// server recommends is one it subsequently rejects.
	if newDefault < newMin {
		return fmt.Errorf("%w: the default chunk is below the minimum", ErrBadRequest)
	}
	storedMin, minErr := num.Narrow[int64](newMin)
	storedDefault, defErr := num.Narrow[int64](newDefault)
	if minErr != nil || defErr != nil {
		return fmt.Errorf("%w: a chunk bound is too large to store", ErrBadRequest)
	}
	if err := e.state.WriteChunkSettings(ctx, storedMin, storedDefault); err != nil {
		return err
	}
	e.settings.store(newMin, newDefault)
	e.settings.override.Store(true)
	return nil
}
