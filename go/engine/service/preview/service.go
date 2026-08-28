//go:build linux

package preview

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// The public API.
//
// The order of one request is fixed and the first two steps are why: validate
// the preset, require the permission, consult the negative cache, consult the
// thumbnail cache, generate, store, answer. The permission check runs before
// any cache lookup, so a cache hit can never be a permission bypass.

// The bounds an exact-size request is clamped to. A caller-chosen size is only
// accepted for the compatibility content route, and only inside these: an
// unbounded size is an unbounded cache and a decode the caller sizes.
const (
	MinSizedDimension = 1
	MaxSizedDimension = 4096
)

// Service handles thumbnail requests.
type Service struct {
	core  *core.Core
	pool  *Pool
	cache *Cache
	clk   clock.Clock
	neg   *Negatives
}

// ServiceOptions holds the service's configuration.
type ServiceOptions struct {
	Core  *core.Core
	Pool  *Pool
	Cache *Cache
	Clock clock.Clock
}

// NewService constructs the service.
func NewService(o ServiceOptions) *Service {
	clk := o.Clock
	if clk == nil {
		clk = clock.System()
	}
	return &Service{core: o.Core, pool: o.Pool, cache: o.Cache, clk: clk, neg: NewNegatives()}
}

// Thumb represents a generated or cached thumbnail.
//
// Closing it belongs to the caller. It is a file rather than bytes because a
// thumbnail streams directly into a response, and buffering one per request
// would occupy memory the server has no need to hold.
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

// Get produces a thumbnail for a resolved path, preferring the cache.
func (s *Service) Get(ctx context.Context, r core.Resolved, preset Preset) (Thumb, error) {
	if !preset.Valid() {
		return Thumb{}, fmt.Errorf("%w: preset %d", ErrUnsupported, preset)
	}
	w, h := preset.Bounds()
	return s.get(ctx, r, preset, w, h, false)
}

// GetSized returns a thumbnail at explicit dimensions.
//
// It is the compatibility content route's seam. The dimensions clamp into the
// supported range and land in the cache key, so a sized preview never collides
// with a preset one or with a different size. It applies the same permission
// check and the same worker path, and it does not stretch a preset result: a
// scaled-up thumbnail of a thumbnail is a blurrier answer than the one the
// caller asked for.
func (s *Service) GetSized(ctx context.Context, r core.Resolved, width, height int) (Thumb, error) {
	w := clampDimension(width)
	h := clampDimension(height)
	// The preset still travels, because it is what the worker sizes against and
	// what the wire carries. The exact box is applied to the result.
	return s.get(ctx, r, presetFor(w, h), w, h, true)
}

// clampDimension holds a caller-chosen size inside the supported range.
func clampDimension(v int) int {
	return min(max(v, MinSizedDimension), MaxSizedDimension)
}

// presetFor is the smallest preset whose box covers an exact request, so the
// worker decodes at a size the result fits inside rather than one it has to be
// scaled up from.
func presetFor(w, h int) Preset {
	want := max(w, h)
	for _, p := range Presets() {
		if pw, _ := p.Bounds(); pw >= want {
			return p
		}
	}
	return PresetLarge
}

// get is the one request path. sized says the width and height are the
// caller's own rather than the preset's, which is what puts them in the key.
func (s *Service) get(
	ctx context.Context, r core.Resolved, preset Preset, width, height int, sized bool,
) (Thumb, error) {
	// The same permission a download requires. A thumbnail derives from the
	// bytes, so viewing one amounts to viewing the file. Checked ahead of any
	// cache lookup, so a planted cache entry provides no way in.
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
		Ident:   ident.Of(r.Share(), st),
		MTimeNs: st.MtimeNs,
		Size:    st.Size,
		Preset:  preset,
	}
	if sized {
		key.Width, key.Height = width, height
	}

	if f, ok := s.cache.Open(key); ok {
		return Thumb{File: f, Preset: preset}, nil
	}
	// A source that has failed before is refused without touching a worker, so
	// a corrupt file in a folder does not cost a worker on every listing.
	if reason, ok := s.neg.Get(key, s.clk.Now()); ok {
		return Thumb{}, negativeError(reason)
	}

	if gerr := s.generate(ctx, r, key, preset); gerr != nil {
		s.neg.Put(key, reasonFor(gerr), s.clk.Now())
		return Thumb{}, gerr
	}

	f, ok := s.cache.Open(key)
	if !ok {
		return Thumb{}, errors.New("preview: the generated thumbnail is not in the cache")
	}
	return Thumb{File: f, Preset: preset}, nil
}

// generate dispatches one job through the pool and stores the outcome.
func (s *Service) generate(ctx context.Context, r core.Resolved, key Key, preset Preset) error {
	in, err := r.Root().OpenRead(r.Path(), vfs.IntentRead)
	if err != nil {
		return core.ErrNotFound
	}
	defer func() {
		//nolint:errcheck // a read descriptor's close has nothing to report to.
		_ = in.Close()
	}()

	// The worker writes into a staging file that the cache publishes via rename,
	// so a partial thumbnail never becomes visible.
	return s.cache.Put(key, func(staged *os.File) error {
		req := Request{
			Kind:      JobImage,
			Preset:    preset,
			Flags:     FlagStripEXIF,
			MaxPixels: maxPixelsFor(),
		}
		if key.Width > 0 && key.Height > 0 {
			// An exact-size request. Both were clamped into 1..MaxSizedDimension
			// on the way in, so each narrowing is proven in range beside it.
			w, werr := num.Narrow[uint16](key.Width)
			h, herr := num.Narrow[uint16](key.Height)
			if werr != nil || herr != nil {
				return fmt.Errorf("%w: a %dx%d box", ErrUnsupported, key.Width, key.Height)
			}
			req.Width, req.Height = w, h
		}
		resp, gerr := s.pool.Generate(ctx, req, vfsSource{in}, staged)
		if gerr != nil {
			return gerr
		}
		if resp.Status != StatusOK {
			return statusError(resp)
		}
		return nil
	})
}

// SweepNegatives drops expired negative entries, which the maintenance loop
// runs.
func (s *Service) SweepNegatives() int { return s.neg.Sweep(s.clk.Now()) }

// reasonFor translates a generation failure into what deserves remembering.
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
	// Anything else says nothing about this file and goes unremembered, since a
	// busy or closed pool would otherwise poison a key.
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

// statusError converts a worker's answer into this package's error set.
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

// vfsSource adapts a share file onto the pool's Source interface.
type vfsSource struct{ f *vfs.File }

func (v vfsSource) File() *os.File { return v.f.OSFile() }

// maxPixelsFor gives the ceiling the parent attaches to a job.
//
// Sending it with the request keeps the limit in one place instead of compiling
// it into two that could disagree. The worker only ever clamps it downward.
func maxPixelsFor() uint32 {
	lim := DefaultDecodeLimits()
	if lim.MaxPixels > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(lim.MaxPixels)
}
