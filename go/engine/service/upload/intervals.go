//go:build linux

package upload

import (
	"fmt"
	"sort"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// What has actually arrived, tracked as a set of ranges.
//
// The set is persisted and never derived from the part file's size: a sparse
// file's size says where the last write landed, not what is in it. A set
// that claimed a range it does not hold would become wrong offset
// arithmetic, and then a hole the client resumes past.

// Range is one received span, half-open.
type Range struct{ Lo, Hi uint64 }

// IntervalSet is the received set in one normal form: sorted, disjoint, and
// coalesced, however the ranges arrived.
type IntervalSet struct {
	runs []Range
}

// NewIntervalSet returns an empty set.
func NewIntervalSet() *IntervalSet { return &IntervalSet{} }

// FullIntervalSet is the set of a file wholly received, which is what
// assembly leaves behind.
func FullIntervalSet(length uint64) *IntervalSet {
	if length == 0 {
		return NewIntervalSet()
	}
	return &IntervalSet{runs: []Range{{Lo: 0, Hi: length}}}
}

// LoadIntervalSet rebuilds a set from stored rows.
//
// The rows are inserted rather than adopted, so an overlapping or unsorted
// pair coalesces into the same normal form a live insert would produce. An
// empty or inverted row is corruption and refuses: a set is the authority on
// what a client may resume past, and one that cannot be believed is worse
// than one that is missing.
func LoadIntervalSet(rows []Range) (*IntervalSet, error) {
	s := NewIntervalSet()
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

// Insert records a received range, merging with every run it overlaps or
// touches.
//
// An empty range changes nothing. An insert that would take the set past the
// run bound refuses and leaves the set exactly as it was: the refusal costs
// the client one chunk, not the session.
func (s *IntervalSet) Insert(lo, hi uint64) error {
	if hi <= lo {
		return nil
	}
	// The bound is checked against what the insert would produce rather than
	// against the current count, because an insert that merges runs together
	// lowers the count and must not be refused.
	if len(s.runs) >= limits.UploadIntervalRuns && !s.touchesExisting(lo, hi) {
		return fmt.Errorf("%w: %d runs is the bound", ErrFragmented, limits.UploadIntervalRuns)
	}

	out := make([]Range, 0, len(s.runs)+1)
	merged := Range{Lo: lo, Hi: hi}
	for _, r := range s.runs {
		switch {
		case r.Hi < merged.Lo || r.Lo > merged.Hi:
			// Disjoint and not touching: kept as it is.
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

// touchesExisting reports whether a range would merge with a run it already
// holds, which is what decides whether the bound applies.
func (s *IntervalSet) touchesExisting(lo, hi uint64) bool {
	for _, r := range s.runs {
		if r.Hi >= lo && r.Lo <= hi {
			return true
		}
	}
	return false
}

// ContiguousPrefix is the resumable offset: the end of the run starting at
// zero, or zero when the set does not start there.
func (s *IntervalSet) ContiguousPrefix() uint64 {
	if len(s.runs) == 0 || s.runs[0].Lo != 0 {
		return 0
	}
	return s.runs[0].Hi
}

// IsComplete reports whether the set covers the whole file. A zero-length
// file is complete when empty, which is the one case where nothing arriving
// is the file having arrived.
func (s *IntervalSet) IsComplete(length uint64) bool {
	if length == 0 {
		return len(s.runs) == 0
	}
	return len(s.runs) == 1 && s.runs[0].Lo == 0 && s.runs[0].Hi >= length
}

// Missing is what a finalize would be waiting for, so a refusal can say what
// to resend rather than only that something is absent.
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

// Received is how many bytes have actually landed, which is not the
// resumable offset once a client has written past a hole.
func (s *IntervalSet) Received() uint64 {
	var n uint64
	for _, r := range s.runs {
		n += r.Hi - r.Lo
	}
	return n
}

// Count reports how many disjoint runs the set contains.
func (s *IntervalSet) Count() int { return len(s.runs) }

// Runs is a copy of the set, for persistence and for a caller that wants to
// look without being able to change it.
func (s *IntervalSet) Runs() []Range { return append([]Range(nil), s.runs...) }

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
