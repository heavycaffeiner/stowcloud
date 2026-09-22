// Package transfer contains transport-neutral primitives for receiving and
// publishing byte ranges. It deliberately has no feature or persistence
// dependencies so upload protocols and storage adapters can share it.
package transfer

import (
	"errors"
	"fmt"
	"sort"
)

// ErrFragmented reports an insert that would exceed the configured run bound.
var ErrFragmented = errors.New("too many disjoint received ranges")

// Range is one received span, half-open: [Lo, Hi).
type Range struct {
	Lo uint64
	Hi uint64
}

type options struct{ maxRuns int }

// Option configures an IntervalSet.
type Option func(*options)

// WithMaxRuns bounds the number of disjoint runs. A non-positive value means
// unlimited; callers that enforce a product limit should inject it explicitly.
func WithMaxRuns(maxRuns int) Option {
	return func(o *options) {
		if maxRuns > 0 {
			o.maxRuns = maxRuns
		}
	}
}

// IntervalSet is a received set in one normal form: sorted, disjoint, and
// coalesced, however the ranges arrived.
type IntervalSet struct {
	runs    []Range
	maxRuns int
}

// NewIntervalSet returns an empty set.
func NewIntervalSet(opts ...Option) *IntervalSet {
	cfg := options{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &IntervalSet{maxRuns: cfg.maxRuns}
}

// FullIntervalSet is the set of a file wholly received.
func FullIntervalSet(length uint64, opts ...Option) *IntervalSet {
	s := NewIntervalSet(opts...)
	if length > 0 {
		s.runs = []Range{{Lo: 0, Hi: length}}
	}
	return s
}

// LoadIntervalSet rebuilds a set from stored rows. Rows are inserted rather
// than adopted, so unsorted or overlapping rows are normalized identically to
// live inserts. Empty and inverted rows are corruption and are refused.
func LoadIntervalSet(rows []Range, opts ...Option) (*IntervalSet, error) {
	s := NewIntervalSet(opts...)
	for _, r := range rows {
		if r.Hi <= r.Lo {
			return nil, fmt.Errorf("a stored received range is %d-%d", r.Lo, r.Hi)
		}
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Insert records a received range, merging every run it overlaps or touches.
// Empty ranges change nothing. A refused insert leaves the set unchanged.
func (s *IntervalSet) Insert(lo, hi uint64) error {
	if hi <= lo {
		return nil
	}
	if s.maxRuns > 0 && len(s.runs) >= s.maxRuns && !s.touchesExisting(lo, hi) {
		return fmt.Errorf("%w: %d runs is the bound", ErrFragmented, s.maxRuns)
	}

	out := make([]Range, 0, len(s.runs)+1)
	merged := Range{Lo: lo, Hi: hi}
	for _, r := range s.runs {
		switch {
		case r.Hi < merged.Lo || r.Lo > merged.Hi:
			out = append(out, r)
		default:
			if r.Lo < merged.Lo {
				merged.Lo = r.Lo
			}
			if r.Hi > merged.Hi {
				merged.Hi = r.Hi
			}
		}
	}
	out = append(out, merged)
	sort.Slice(out, func(i, j int) bool { return out[i].Lo < out[j].Lo })
	s.runs = out
	return nil
}

func (s *IntervalSet) touchesExisting(lo, hi uint64) bool {
	for _, r := range s.runs {
		if r.Hi >= lo && r.Lo <= hi {
			return true
		}
	}
	return false
}

// ContiguousPrefix is the resumable offset: the end of the run starting at 0,
// or zero when the set does not start there.
func (s *IntervalSet) ContiguousPrefix() uint64 {
	if len(s.runs) == 0 || s.runs[0].Lo != 0 {
		return 0
	}
	return s.runs[0].Hi
}

// IsComplete reports whether the set covers the whole file.
func (s *IntervalSet) IsComplete(length uint64) bool {
	if length == 0 {
		return len(s.runs) == 0
	}
	return len(s.runs) == 1 && s.runs[0].Lo == 0 && s.runs[0].Hi >= length
}

// Missing returns the uncovered ranges below length.
func (s *IntervalSet) Missing(length uint64) []Range {
	var out []Range
	var at uint64
	for _, r := range s.runs {
		if r.Lo > at {
			out = append(out, Range{Lo: at, Hi: min64(r.Lo, length)})
		}
		if r.Hi > at {
			at = r.Hi
		}
		if at >= length {
			return out
		}
	}
	if at < length {
		out = append(out, Range{Lo: at, Hi: length})
	}
	return out
}

// Received returns the bytes covered by the set.
func (s *IntervalSet) Received() uint64 {
	var n uint64
	for _, r := range s.runs {
		n += r.Hi - r.Lo
	}
	return n
}

// Count reports how many disjoint runs the set contains.
func (s *IntervalSet) Count() int { return len(s.runs) }

// Runs returns a copy of the normalized set.
func (s *IntervalSet) Runs() []Range { return append([]Range(nil), s.runs...) }

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
