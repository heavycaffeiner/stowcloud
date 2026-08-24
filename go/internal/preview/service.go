//go:build linux

package preview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The public API.

// Service answers thumbnail requests.
type Service struct {
	core  *core.Core
	pool  *Pool
	cache *Cache
	clk   clock.Clock

	// negMu guards the negative cache, which every request touches.
	negMu sync.Mutex
	neg   *Negatives
}

// ServiceOptions configures the service.
type ServiceOptions struct {
	Core  *core.Core
	Pool  *Pool
	Cache *Cache
	Clock clock.Clock
}

// NewService builds the service.
func NewService(o ServiceOptions) *Service {
	clk := o.Clock
	if clk == nil {
		clk = clock.System()
	}
	return &Service{core: o.Core, pool: o.Pool, cache: o.Cache, clk: clk, neg: NewNegatives()}
}

// Thumb is a generated or cached thumbnail.
//
// The caller closes it. It is a file rather than bytes because a thumbnail is
// served straight to a response and buffering one per request is memory the
// server does not need to hold.
type Thumb struct {
	File   *os.File
	Preset Preset
}

// Close releases the thumbnail.
func (t Thumb) Close() error {
	if t.File == nil {
		return nil
	}
	return t.File.Close()
}

// Get returns a thumbnail for a resolved path, from cache when possible.
//
// A source that has failed before is refused from the negative cache without
// touching a worker, so a corrupt file in a folder does not cost a worker on
// every listing.
func (s *Service) Get(ctx context.Context, r core.Resolved, preset Preset) (Thumb, error) {
	if !preset.valid() {
		return Thumb{}, fmt.Errorf("%w: preset %d", ErrUnsupported, preset)
	}
	// The same permission a download needs. A thumbnail is a derivative of the
	// bytes, so being able to see one is being able to see the file.
	if err := r.Require(acl.Read | acl.Download); err != nil {
		return Thumb{}, err
	}

	st, err := r.Root().Stat(r.Path())
	if err != nil {
		return Thumb{}, core.ErrNotFound
	}
	if st.Kind.IsDir() {
		return Thumb{}, fmt.Errorf("%w: a directory has no thumbnail", ErrUnsupported)
	}

	key := Key{
		Ident:   identOf(r, st),
		MTimeNs: st.MtimeNs,
		Size:    st.Size,
		Preset:  preset,
	}

	if f, ok := s.cache.Open(key); ok {
		return Thumb{File: f, Preset: preset}, nil
	}
	if reason, ok := s.remembered(key); ok {
		return Thumb{}, negativeError(reason)
	}

	if err := s.generate(ctx, r, key, preset); err != nil {
		s.remember(key, reasonFor(err))
		return Thumb{}, err
	}

	f, ok := s.cache.Open(key)
	if !ok {
		return Thumb{}, errors.New("preview: the generated thumbnail is not in the cache")
	}
	return Thumb{File: f, Preset: preset}, nil
}

// generate runs one job through the pool and stores the result.
func (s *Service) generate(ctx context.Context, r core.Resolved, key Key, preset Preset) error {
	in, err := r.Root().OpenRead(r.Path(), vfs.IntentRead)
	if err != nil {
		return core.ErrNotFound
	}
	defer func() {
		if cerr := in.Close(); cerr != nil {
			_ = cerr //nolint:errcheck // a read descriptor's close has nothing to report to.
		}
	}()

	// The worker writes into a staged file that the cache publishes by rename,
	// so a partial thumbnail is never visible.
	return s.cache.Put(key, func(staged *os.File) error {
		resp, gerr := s.pool.Generate(ctx, Request{
			Kind:      JobImage,
			Preset:    preset,
			Flags:     FlagStripEXIF,
			MaxPixels: maxPixelsFor(),
		}, vfsSource{in}, staged)
		if gerr != nil {
			return gerr
		}
		if resp.Status != StatusOK {
			return statusError(resp)
		}
		return nil
	})
}

// remembered reports a cached failure.
func (s *Service) remembered(key Key) (Negative, bool) {
	s.negMu.Lock()
	defer s.negMu.Unlock()
	return s.neg.Get(key, s.clk.Now())
}

func (s *Service) remember(key Key, reason Negative) {
	if reason == NegativeNone {
		return
	}
	s.negMu.Lock()
	defer s.negMu.Unlock()
	s.neg.Put(key, reason, s.clk.Now())
}

// SweepNegatives drops expired negative entries, which the idle gate runs.
func (s *Service) SweepNegatives() int {
	s.negMu.Lock()
	defer s.negMu.Unlock()
	return s.neg.Sweep(s.clk.Now())
}

// reasonFor maps a generation failure onto what is worth remembering.
func reasonFor(err error) Negative {
	switch {
	case errors.Is(err, ErrTooLarge):
		return NegativeTooLarge
	case errors.Is(err, ErrUnsupported):
		return NegativeUnsupported
	case errors.Is(err, ErrNotImplemented):
		return NegativeNotImplemented
	case errors.Is(err, ErrDecode):
		return NegativeDecodeFailed
	case errors.Is(err, ErrWorkerDied):
		return NegativeWorkerDied
	}
	// Everything else is not a statement about this file, so it is not
	// remembered: a busy pool or a closed one would otherwise poison a key.
	return NegativeNone
}

func negativeError(reason Negative) error {
	switch reason {
	case NegativeTooLarge:
		return ErrTooLarge
	case NegativeUnsupported:
		return ErrUnsupported
	case NegativeNotImplemented:
		return ErrNotImplemented
	case NegativeDecodeFailed:
		return ErrDecode
	case NegativeWorkerDied:
		return ErrWorkerDied
	}
	return nil
}

// statusError turns a worker's answer into this package's error set.
func statusError(resp Response) error {
	switch resp.Status {
	case StatusTooLarge:
		return fmt.Errorf("%w: %s", ErrTooLarge, resp.Err)
	case StatusUnsupported:
		return fmt.Errorf("%w: %s", ErrUnsupported, resp.Err)
	case StatusNotImplemented:
		return fmt.Errorf("%w: %s", ErrNotImplemented, resp.Err)
	case StatusDecodeFailed:
		return fmt.Errorf("%w: %s", ErrDecode, resp.Err)
	case StatusInternal:
		return fmt.Errorf("preview: the worker failed: %s", resp.Err)
	}
	return nil
}

// vfsSource adapts a share file to the pool's Source.
type vfsSource struct{ f *vfs.File }

func (v vfsSource) File() *os.File { return v.f.OSFile() }

// maxPixelsFor is the ceiling the parent sends with a job.
//
// It travels with the request so the limit lives in one place rather than
// being compiled into two that can disagree. The worker clamps it downward and
// never upward.
func maxPixelsFor() uint32 {
	lim := DefaultDecodeLimits()
	if lim.MaxPixels > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(lim.MaxPixels)
}

func identOf(r core.Resolved, st vfs.Stat) cache.Ident {
	return cache.IdentOf(r.Share(), st)
}
