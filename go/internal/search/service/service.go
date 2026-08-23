//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The service: which tier answers a query.

// ErrCanceled is the caller's context ending. It is not a failure and is not
// logged as one.
var ErrCanceled = errors.New("search: the query was cancelled")

// ErrBusy is the concurrency gate refusing. Search sweeps whole trees, so
// letting every request start one is how interactive listings get starved.
var ErrBusy = errors.New("search: too many searches already running")

// Tier says what answered, so a caller can surface "this was a full scan"
// rather than leaving a slow query unexplained.
type Tier int

const (
	// TierWalk is the parallel walk.
	TierWalk Tier = iota
	// TierIndex is the trigram index.
	TierIndex
)

func (t Tier) String() string {
	if t == TierIndex {
		return "index"
	}
	return "walk"
}

// StorageClass moves two numbers, because a walk on an NVMe array and a walk
// on a cold rotational one are different operations wearing one name.
type StorageClass int

const (
	StorageSSD StorageClass = iota
	StorageRotational
)

// Concurrency is how many searches may run at once on this storage.
//
// A lower number on rotational media is not a smaller allowance, it is a
// larger one: four concurrent seek-bound walks on one array finish later than
// two do, and they take the interactive requests down with them.
func (s StorageClass) Concurrency() int {
	if s == StorageRotational {
		return limits.ConcurrentSearchesRotational
	}
	return limits.ConcurrentSearchesSSD
}

// Deadline is how long a walk may take here. The longer one on rotational
// media is the matching admission that the work genuinely takes longer.
func (s StorageClass) Deadline() time.Duration {
	if s == StorageRotational {
		return limits.SearchWalkDeadlineRotational
	}
	return limits.SearchWalkDeadlineSSD
}

// Threads is the walk's worker count for this storage. Rotational media thrash
// on seeks when over-parallelised.
func (s StorageClass) Threads(cpus int) int {
	if s == StorageRotational {
		return 2
	}
	if cpus < 1 {
		cpus = 4
	}
	if cpus > 16 {
		cpus = 16
	}
	return cpus
}

// Options configures the service.
type Options struct {
	Clock   clock.Clock
	Storage StorageClass
	CPUs    int
	// Index is optional. Nil means the index is off, which is the default: it
	// is an escalation taken when measurement says the walk is not enough.
	Index *index.NameIndex
}

// Service answers queries.
type Service struct {
	clk     clock.Clock
	storage StorageClass
	cpus    int

	mu    sync.Mutex
	ix    *index.NameIndex
	slots chan struct{}
}

// New builds the service.
func New(o Options) *Service {
	clk := o.Clock
	if clk == nil {
		clk = clock.System()
	}
	s := &Service{clk: clk, storage: o.Storage, cpus: o.CPUs, ix: o.Index}
	s.slots = make(chan struct{}, o.Storage.Concurrency())
	return s
}

// SetIndex attaches or detaches the index at runtime, which is what the
// administrator's switch does.
func (s *Service) SetIndex(ix *index.NameIndex) {
	s.mu.Lock()
	s.ix = ix
	s.mu.Unlock()
}

// HasIndex reports whether a name index is attached, which is what decides
// whether a query is answered from the index or from a walk.
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
	// WithMetadata resolves size and time for the entries that survive
	// filtering. A name-only query leaves it off.
	WithMetadata bool
}

// Results is what a query produced.
type Results struct {
	Hits []search.Hit
	// Tier says which one answered.
	Tier Tier
	// Fallback is why the index declined, when it did. It is reported so an
	// operator can see that an index exists and did not help.
	Fallback index.FallbackReason
	// Truncated reports that the limit cut the result.
	Truncated bool
	// Deadline reports that the walk ran out of time and the result is
	// partial. It is flagged rather than raised: a partial answer now beats an
	// error after eight seconds.
	Deadline bool
	Elapsed  time.Duration
}

// Query runs a search across the sources the caller may see.
//
// The index answers where the estimator says it is worth it and the index does
// not decline; otherwise the walk does.
func (s *Service) Query(ctx context.Context, sources []search.Source, opt QueryOptions) (Results, error) {
	if len(opt.Query) > limits.SearchQueryBytes {
		return Results{}, limits.Exceed("search query", limits.SearchQueryBytes, int64(len(opt.Query)))
	}
	if opt.Limit <= 0 || opt.Limit > limits.SearchResults {
		opt.Limit = limits.SearchResults
	}

	// The gate is taken before any work starts, so a refused search costs a
	// channel send rather than a directory read.
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
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
			// The index is a cache: a corrupt segment costs speed, never
			// answers, so the query continues on the walk.
		case res.MustFallBack():
			// The index said it cannot narrow this query. The walk runs, and
			// the reason is reported so it is not mistaken for an empty
			// result.
			out, werr := s.walk(ctx, sources, needle, opt, start)
			out.Fallback = res.Fallback
			return out, werr
		default:
			return Results{
				Hits:      s.promote(ctx, sources, res.Hits, opt),
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
	deadline := s.storage.Deadline()
	wctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	res, err := search.Walk(wctx, sources, search.WalkOptions{
		Needle:       needle,
		Limit:        opt.Limit,
		Scope:        opt.Scope,
		Threads:      s.storage.Threads(s.cpus),
		WithMetadata: opt.WithMetadata,
		NowNs:        s.clk.Now().UnixNano(),
	})
	if err != nil {
		// The caller's own cancellation is an error; the deadline is not,
		// because a partial answer is what the deadline exists to produce.
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

// pathUnder turns an index-stored path back into a validated path under the
// source's base. The index stores what it was given, so this is a trust
// boundary: a stored path is re-validated rather than trusted to still be
// legal.
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

// promote turns bare index hits into full results.
//
// The index stores names only. A hit becomes a result through a stat performed
// after the caller's permission check, and that stat doubles as the staleness
// check: an entry for a file that no longer exists is dropped rather than
// returned.
func (s *Service) promote(ctx context.Context, sources []search.Source, hits []index.Hit, opt QueryOptions) []search.Hit {
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
		if !ok {
			// A share this caller cannot see. Dropping it here is the same
			// existence rule the walk applies before scoring.
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
			// The index is stale: the file is gone. Dropping it is the
			// revalidation working.
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
			size, mtime := st.Size, st.MtimeNs
			hit.Size, hit.MTimeNs = &size, &mtime
		}
		out = append(out, hit)
	}
	return out
}
