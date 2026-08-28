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

// Settings is the live chunk floor and default: the two numbers that can move
// after the engine is built, without a restart.
//
// Plain atomics rather than a mutex. Every reader wants the current pair,
// nothing coordinates the two fields against each other, and a reader that
// sees one write's floor beside another's default is no different from two
// sessions created a moment apart.
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
		// A stored pair that will not read is not a reason to refuse to start:
		// the configuration's values are a working fallback and the override
		// is reported as absent, which is what it now effectively is.
		return s, nil
	}
	s.store(minBytes, defBytes)
	s.override.Store(true)
	return s, nil
}

// store applies the floor to both numbers on the way in, so no path can leave
// a live value below the compiled-in minimum.
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

// Min is the live floor a mid-stream chunk is measured against.
func (s *Settings) Min() uint64 { return s.minBytes.Load() }

// Default is the live advertised chunk size. It is a recommendation and never
// a requirement: not enforcing a ceiling does not make middleboxes disappear,
// it means this server is not the one refusing.
func (s *Settings) Default() uint64 { return s.defaultBytes.Load() }

// Snapshot is both numbers, for a caller that stores them together.
func (s *Settings) Snapshot() (minBytes, defaultBytes uint64) {
	return s.minBytes.Load(), s.defaultBytes.Load()
}

// Overridden reports whether an administrator has ever written these, as
// opposed to them coming from the configuration. It is the only thing that
// can answer that for the settings screen.
func (s *Settings) Overridden() bool { return s.override.Load() }

// ApplySettings stores an administrator's chunk bounds and makes them live.
//
// It is the one write path: the second one that used to exist did the same
// job and only this one had a caller outside a test.
//
// The order is load-bearing. A validation failure or a failed disk write
// never lets the in-memory pair drift from what is on disk, which is what a
// restart would then read back. A nil value leaves that half as it was, so a
// screen editing one field does not silently reset the other.
//
// It does not touch a session in flight: both numbers are snapshotted once at
// creation, so raising the floor cannot retroactively refuse a chunk that was
// legal when the session started.
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
	// A default below the minimum is a configuration where every chunk the
	// server suggests is one it then refuses.
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
