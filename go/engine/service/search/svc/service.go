//go:build linux

// Package svc chooses which tier answers a search, fills the index, and keeps
// it current.
//
// The walk is the base tier and always works. The index is an escalation and a
// cache: it answers when it can, declines when it cannot, and a broken one
// costs speed rather than answers.
package svc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
)

// ErrCanceled reports the caller's context ending. It is not a failure and is
// not logged as one.
var ErrCanceled = errors.New("search: the query was cancelled")

// ErrBusy reports the concurrency gate declining. Search traverses entire trees,
// so allowing every request to begin one is how interactive listings starve.
var ErrBusy = errors.New("search: too many searches already running")

// Tier identifies what produced the answer, letting a caller explain that a
// query ran as a full scan instead of leaving its slowness unaccounted for.
type Tier int

const (
	// TierWalk denotes the parallel walk.
	TierWalk Tier = iota
	// TierIndex denotes the trigram index.
	TierIndex
)

func (t Tier) String() string {
	if t == TierIndex {
		return "index"
	}
	return "walk"
}

// threadsFor sets the walk's worker count from the reported CPU count, bounded
// at both ends so a container with an inflated or absent count still gets a
// sane worker pool.
func threadsFor(cpus int) int {
	if cpus < 1 {
		cpus = 4
	}
	if cpus > 16 {
		cpus = 16
	}
	return cpus
}

// Options holds the service's configuration.
type Options struct {
	Clock clock.Clock
	CPUs  int
	// Index is optional, and nil disables it, which is the default. Enabling it
	// is an escalation taken once measurement shows the walk is insufficient.
	Index       *index.NameIndex
	Concurrency int
}

// Service answers queries.
type Service struct {
	clk  clock.Clock
	cpus int

	mu    sync.Mutex
	ix    *index.NameIndex
	slots chan struct{}

	// The bounds an administrator adjusts from the settings screen. They are held
	// here rather than read from the compiled-in limits, because a value the
	// screen changed must be the one the next query uses. A setting that is
	// stored and never read makes the screen report a change that happened
	// nowhere.
	//
	// Zero selects the compiled-in default, so a build that never configures
	// them behaves exactly as before.
	concurrency int
	deadline    time.Duration
}

// New builds the service.
func New(o Options) *Service {
	clk := o.Clock
	if clk == nil {
		clk = clock.System()
	}
	concurrency := o.Concurrency
	if concurrency <= 0 {
		concurrency = limits.ConcurrentSearches
	}
	s := &Service{
		clk:         clk,
		cpus:        o.CPUs,
		ix:          o.Index,
		concurrency: o.Concurrency,
		slots:       make(chan struct{}, concurrency),
	}
	return s
}

// SetBounds adjusts the query bounds, which is what the settings screen's search
// section drives. Zero leaves a field at the compiled-in default.
func (s *Service) SetBounds(concurrency int, deadline time.Duration) {
	s.mu.Lock()
	s.concurrency, s.deadline = concurrency, deadline
	targetCap := concurrency
	if targetCap <= 0 {
		targetCap = limits.ConcurrentSearches
	}
	if targetCap != cap(s.slots) {
		s.slots = make(chan struct{}, targetCap)
	}
	s.mu.Unlock()
}

// Bounds is what the settings screen last set, for a screen that reads back
// what it wrote. Zero in a field means the compiled-in default applies.
func (s *Service) Bounds() (concurrency int, deadline time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.concurrency, s.deadline
}

// walkDeadline bounds how long a walk may run, and an administrator may adjust
// it.
func (s *Service) walkDeadline() time.Duration {
	s.mu.Lock()
	d := s.deadline
	s.mu.Unlock()
	if d > 0 {
		return d
	}
	return limits.SearchWalkDeadline
}

// SetIndex attaches or detaches the index while running, which is what the
// administrator's switch controls.
func (s *Service) SetIndex(ix *index.NameIndex) {
	s.mu.Lock()
	s.ix = ix
	s.mu.Unlock()
}

// HasIndex reports whether a name index is attached, which determines whether a
// query is served from the index or from a walk.
func (s *Service) HasIndex() bool { return s.index() != nil }

func (s *Service) index() *index.NameIndex {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ix
}

// QueryOptions is one search.
type QueryOptions struct {
	Query string
	Limit int
	Scope string
	// WithMetadata resolves size and time for entries surviving the filter. A
	// name-only query leaves it disabled.
	WithMetadata bool
}

// Results holds what a query produced.
type Results struct {
	Hits []search.Hit
	// Tier says which one answered.
	Tier Tier
	// Fallback explains why the index declined, where it did. Reporting it lets
	// an operator see that an index exists and did not contribute.
	Fallback index.FallbackReason
	// Truncated indicates the limit shortened the result.
	Truncated bool
	// Deadline indicates the walk exhausted its time and the result is partial.
	// It is flagged rather than raised as an error, since a partial answer now
	// beats an error eight seconds later.
	Deadline bool
	Elapsed  time.Duration
}

// Query searches across the sources visible to the caller.
//
// The index answers unless it declines, in which case the walk does.
func (s *Service) Query(ctx context.Context, sources []search.Source, opt QueryOptions) (Results, error) {
	if len(opt.Query) > limits.SearchQueryBytes {
		return Results{}, limits.Exceed("search query", limits.SearchQueryBytes, int64(len(opt.Query)))
	}
	if opt.Limit <= 0 || opt.Limit > limits.SearchResults {
		opt.Limit = limits.SearchResults
	}

	// The gate is acquired before any work begins, so a rejected search costs a
	// channel send instead of a directory read.
	s.mu.Lock()
	slots := s.slots
	s.mu.Unlock()

	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return Results{}, ErrCanceled
	default:
		return Results{}, ErrBusy
	}
	start := s.clk.Now()
	needle := search.FoldString(opt.Query)

	if ix := s.index(); ix != nil {
		res, err := ix.Query([]byte(opt.Query), opt.Limit)
		switch {
		case err != nil:
			// The index is a cache, so a corrupt segment costs speed and never
			// correctness; the query proceeds by walking.
		case res.MustFallBack():
			// The index reported it cannot narrow this query. The walk runs,
			// and the reason is surfaced so nobody mistakes it for an empty
			// result.
			out, werr := s.walk(ctx, sources, needle, opt, start)
			out.Fallback = res.Fallback
			return out, werr
		default:
			hits := s.promote(ctx, sources, res.Hits, opt)
			return Results{
				Hits:      hits,
				Tier:      TierIndex,
				Truncated: len(res.Hits) >= opt.Limit,
				Elapsed:   s.clk.Now().Sub(start),
			}, nil
		}
	}

	return s.walk(ctx, sources, needle, opt, start)
}

func (s *Service) walk(
	ctx context.Context, sources []search.Source, needle []byte, opt QueryOptions, start time.Time,
) (Results, error) {
	deadline := s.walkDeadline()
	wctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	res, err := search.Walk(wctx, sources, search.WalkOptions{
		Needle:       needle,
		Limit:        opt.Limit,
		Scope:        opt.Scope,
		Threads:      threadsFor(s.cpus),
		WithMetadata: opt.WithMetadata,
		NowNs:        s.clk.Now().UnixNano(),
	})
	if err != nil {
		// Cancellation by the caller is an error while the deadline is not, since
		// producing a partial answer is exactly what the deadline exists for.
		if ctx.Err() != nil {
			return Results{}, ErrCanceled
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return Results{}, fmt.Errorf("search: the walk failed: %w", err)
		}
	}

	return Results{
		Hits:      res.Hits,
		Tier:      TierWalk,
		Truncated: res.Truncated,
		Deadline:  wctx.Err() != nil && ctx.Err() == nil,
		Elapsed:   s.clk.Now().Sub(start),
	}, nil
}

// pathUnder converts an index-stored path back into a validated path beneath the
// source's base.
//
// This is the trust boundary the entire index depends on. Index rows describe
// yesterday's filesystem while today's is authoritative, so a stored path is
// revalidated rather than assumed still legal, and nothing read from disk
// reaches a client without meeting the live tree again.
func pathUnder(src search.Source, stored string) (vfs.SafePath, error) {
	p := src.Base
	for _, comp := range strings.Split(stored, "/") {
		if comp == "" {
			continue
		}
		next, err := p.JoinExisting(comp)
		if err != nil {
			return vfs.SafePath{}, err
		}
		p = next
	}
	if !p.Under(src.Base) {
		return vfs.SafePath{}, errors.New("search: an indexed path outside its source")
	}
	return p, nil
}

// promote expands bare index hits into full results.
//
// Only names live in the index. A hit becomes a result via a stat run after the
// caller's permission check, and that stat serves as the staleness check too: an
// entry for a file that no longer exists is dropped rather than returned.
func (s *Service) promote(
	ctx context.Context, sources []search.Source, hits []index.Hit, opt QueryOptions,
) []search.Hit {
	byShare := map[uint32]search.Source{}
	for _, src := range sources {
		byShare[src.Share] = src
	}

	out := make([]search.Hit, 0, len(hits))
	for _, h := range hits {
		if ctx.Err() != nil {
			return out
		}
		src, ok := byShare[h.Share]
		if !ok || src.Root == nil {
			// A share this caller cannot see, or one that is broken. Dropping
			// it here is the same existence rule the walk applies before
			// scoring.
			continue
		}
		p, err := pathUnder(src, h.Path)
		if err != nil {
			continue
		}
		if src.Allow != nil && !src.Allow(p, false) {
			continue
		}

		st, serr := src.Root.Stat(p)
		if serr != nil {
			// The index is stale and the file has gone. Dropping it is
			// revalidation doing its job.
			continue
		}

		hit := search.Hit{
			Share: h.Share,
			Path:  src.Prefix + p.String(),
			Name:  h.Name,
			IsDir: st.Kind.IsDir(),
			Score: h.Score,
		}
		if opt.WithMetadata {
			size := st.Size
			mtime := st.MtimeNs
			hit.Size = &size
			hit.MTimeNs = &mtime
		}
		out = append(out, hit)
	}
	return out
}
