// Package upload is the resumable upload engine: TUS, the interval set that
// records which byte ranges have landed, the ordering rule, the two spool
// modes, checksum verification and the orphan sweep.
//
// Two invariants are absolute here and both are about not producing a
// half-thing. A partial upload can never appear at the destination, which is
// why publication is a rename rather than a stream into the destination. And
// no staging file is ever read back and rewritten, which is why assembly uses
// copy_file_range rather than a copy loop.
package upload

import (
	"errors"
	"fmt"
	"slices"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// Range is one half-open byte range, [Lo, Hi).
type Range struct {
	Lo uint64
	Hi uint64
}

// ErrFragmented is an insert that would push the run count past the bound. The
// set is left exactly as it was, so a refusal costs the client the one chunk
// rather than the session.
var ErrFragmented = errors.New("too many disjoint received ranges")

// IntervalSet is the sorted, coalescing set of byte ranges one session has
// received. Inserting merges with every neighbour that overlaps or touches, so
// the set has exactly one normal form for a given collection of ranges however
// they arrived.
//
// It is the answer to "where should I resume", and it is also the answer to
// "is this file finished". That second question is why it is persisted rather
// than derived from the part file's size: a sparse file's size says where the
// last write landed, not what is in it.
type IntervalSet struct {
	runs []Range
}

// NewIntervalSet is the empty set.
func NewIntervalSet() *IntervalSet { return &IntervalSet{} }

// FullIntervalSet is the set covering [0, length), which is what an assembled
// name-ordered session holds once its chunks are in place.
func FullIntervalSet(length uint64) *IntervalSet {
	if length == 0 {
		return &IntervalSet{}
	}
	return &IntervalSet{runs: []Range{{Lo: 0, Hi: length}}}
}

// LoadIntervalSet rebuilds a set from stored rows, re-deriving the invariant
// rather than trusting it. These are bytes read back from a database, and a
// set that claims a range it does not hold turns into wrong offset arithmetic
// and then into a hole the client resumes past.
//
// Rows are inserted rather than adopted, so an overlapping or unsorted pair
// coalesces into the same normal form a live insert would have produced.
func LoadIntervalSet(rows []Range) (*IntervalSet, error) {
	s := NewIntervalSet()
	for _, r := range rows {
		if r.Hi <= r.Lo {
			return nil, fmt.Errorf("a stored range %d..%d is empty or inverted", r.Lo, r.Hi)
		}
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Insert records [lo, hi), merging with every run it overlaps or touches.
//
// An empty range is accepted and changes nothing: a zero-length write is the
// caller's business to refuse, and the set has nothing to record either way.
// A range that would take the run count past the bound is refused and the set
// is left untouched.
func (s *IntervalSet) Insert(lo, hi uint64) error {
	if hi <= lo {
		return nil
	}
	// The first run that could touch or overlap [lo, hi): everything before it
	// ends strictly below lo and cannot merge.
	i := 0
	for i < len(s.runs) && s.runs[i].Hi < lo {
		i++
	}
	j := i
	merged := Range{Lo: lo, Hi: hi}
	for j < len(s.runs) && s.runs[j].Lo <= merged.Hi {
		merged.Lo = min64(merged.Lo, s.runs[j].Lo)
		merged.Hi = max64(merged.Hi, s.runs[j].Hi)
		j++
	}
	if len(s.runs)-(j-i)+1 > limits.UploadIntervalRuns {
		return fmt.Errorf("%w: the bound is %d runs", ErrFragmented, limits.UploadIntervalRuns)
	}
	s.runs = slices.Replace(s.runs, i, j, merged)
	return nil
}

// ContiguousPrefix is the end of the run starting at zero, and zero when there
// is none. It is the resumable offset: a client that asks after a failed chunk
// is told the truth rather than the part file's size.
func (s *IntervalSet) ContiguousPrefix() uint64 {
	if len(s.runs) == 0 || s.runs[0].Lo != 0 {
		return 0
	}
	return s.runs[0].Hi
}

// IsComplete reports whether the set covers every byte of [0, length).
func (s *IntervalSet) IsComplete(length uint64) bool {
	if length == 0 {
		return len(s.runs) == 0
	}
	return len(s.runs) == 1 && s.runs[0].Lo == 0 && s.runs[0].Hi == length
}

// Missing names the ranges of [0, length) the set does not hold, which is what
// a refused finalize reports so the client knows what to resend.
func (s *IntervalSet) Missing(length uint64) []Range {
	var out []Range
	var at uint64
	for _, r := range s.runs {
		if r.Lo >= length {
			break
		}
		if r.Lo > at {
			out = append(out, Range{Lo: at, Hi: min64(r.Lo, length)})
		}
		at = max64(at, r.Hi)
	}
	if at < length {
		out = append(out, Range{Lo: at, Hi: length})
	}
	return out
}

// Runs returns a copy. The set is normalised by construction and handing out
// the backing slice hands out a normal form a caller can break.
func (s *IntervalSet) Runs() []Range { return slices.Clone(s.runs) }

// Count is how many disjoint runs the set holds.
func (s *IntervalSet) Count() int { return len(s.runs) }

// Received is the total number of bytes the set covers, which is not the same
// as the resumable offset once a client has written past a hole.
func (s *IntervalSet) Received() uint64 {
	var n uint64
	for _, r := range s.runs {
		n += r.Hi - r.Lo
	}
	return n
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
