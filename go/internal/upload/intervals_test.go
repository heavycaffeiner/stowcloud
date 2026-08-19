package upload

import (
	"errors"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// randomness here is a small deterministic generator rather than math/rand,
// which D9 refuses tree-wide, and rather than crypto/rand, which would make
// a failing case impossible to reproduce from the output. A property test
// wants the same sequence on every run: a failure nobody can rerun is a
// failure nobody can fix.
type seeded struct{ state uint64 }

func newSeeded(seed uint64) *seeded { return &seeded{state: seed | 1} }

// next is xorshift64*, which is four lines and distributes well enough to
// build test inputs from.
func (r *seeded) next() uint64 {
	r.state ^= r.state >> 12
	r.state ^= r.state << 25
	r.state ^= r.state >> 27
	return r.state * 2685821657736338717
}

// intn takes and returns a uint64, so nothing in the callers has to convert
// between a signed count and an unsigned byte offset to build a range.
func (r *seeded) intn(n uint64) uint64 { return r.next() % n }

// shuffle works in uint64 throughout, so the index never crosses between a
// signed and an unsigned width. The caller's slice length is what bounds it.
func (r *seeded) shuffle(n uint64, swap func(i, j uint64)) {
	for i := n; i > 1; i-- {
		swap(i-1, r.intn(i))
	}
}

// The two properties the phase brief names, plus the refusals either side of
// them. The set is the foundation everything else in this package stands on:
// a wrong answer here is a hole the client resumes past, which is silent
// corruption rather than a failed upload.

func TestInsertMergesTouchingAndOverlapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []Range
		want []Range
	}{
		{"touching", []Range{{0, 5}, {5, 10}}, []Range{{0, 10}}},
		{"overlapping", []Range{{0, 10}, {5, 15}}, []Range{{0, 15}}},
		{"disjoint", []Range{{0, 5}, {10, 15}}, []Range{{0, 5}, {10, 15}}},
		{"engulfing", []Range{{5, 10}, {0, 20}}, []Range{{0, 20}}},
		{"contained", []Range{{0, 20}, {5, 10}}, []Range{{0, 20}}},
		{"bridging", []Range{{0, 5}, {10, 15}, {5, 10}}, []Range{{0, 15}}},
		{"empty is a no-op", []Range{{0, 5}, {7, 7}}, []Range{{0, 5}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewIntervalSet()
			for _, r := range tc.in {
				if err := s.Insert(r.Lo, r.Hi); err != nil {
					t.Fatalf("Insert(%d, %d): %v", r.Lo, r.Hi, err)
				}
			}
			if got := s.Runs(); !slices.Equal(got, tc.want) {
				t.Fatalf("runs = %v, want %v", got, tc.want)
			}
		})
	}
}

// The first property: insertion order does not decide the result. A set that
// disagreed with itself depending on the order chunks arrived in would report
// a different resumable offset to two clients that sent the same bytes.
func TestPropertyInsertionOrderDoesNotMatter(t *testing.T) {
	rng := newSeeded(0x5DEECE66D)
	for trial := 0; trial < 200; trial++ {
		n := 1 + rng.intn(40)
		ranges := make([]Range, 0, n)
		for i := uint64(0); i < n; i++ {
			lo := rng.intn(2000)
			ranges = append(ranges, Range{Lo: lo, Hi: lo + 1 + rng.intn(50)})
		}

		want := buildSet(t, ranges).Runs()
		for shuffle := 0; shuffle < 4; shuffle++ {
			other := slices.Clone(ranges)
			rng.shuffle(uint64(len(other)), func(i, j uint64) { other[i], other[j] = other[j], other[i] })
			if got := buildSet(t, other).Runs(); !slices.Equal(got, want) {
				t.Fatalf("a reordering converged elsewhere:\n got %v\nwant %v", got, want)
			}
		}
	}
}

// The second property: a set that reports complete covers every byte, and one
// that does not names what is missing. Completeness is what finalize gates on,
// so a set that says yes over a hole publishes a corrupt file.
func TestPropertyCompleteCoversEveryByte(t *testing.T) {
	rng := newSeeded(0x1234ABCD)
	for trial := 0; trial < 200; trial++ {
		length := 1 + rng.intn(500)
		s := NewIntervalSet()
		// Chop [0, length) into pieces and insert them in a random order, so
		// completeness arrives from every direction rather than left to right.
		var cuts []uint64
		for at := uint64(0); at < length; {
			at += 1 + rng.intn(30)
			cuts = append(cuts, min64(at, length))
		}
		pieces := make([]Range, 0, len(cuts))
		var prev uint64
		for _, c := range cuts {
			pieces = append(pieces, Range{Lo: prev, Hi: c})
			prev = c
		}
		rng.shuffle(uint64(len(pieces)), func(i, j uint64) { pieces[i], pieces[j] = pieces[j], pieces[i] })

		for i, p := range pieces {
			if err := s.Insert(p.Lo, p.Hi); err != nil {
				t.Fatalf("Insert(%d, %d): %v", p.Lo, p.Hi, err)
			}
			last := i == len(pieces)-1
			if got := s.IsComplete(length); got != last {
				t.Fatalf("IsComplete after %d of %d pieces = %v", i+1, len(pieces), got)
			}
			// Whatever the set reports as missing must be exactly the bytes it
			// does not hold, so a refused finalize names something real.
			covered := coveredBytes(s, length)
			for _, m := range s.Missing(length) {
				for b := m.Lo; b < m.Hi; b++ {
					if covered[b] {
						t.Fatalf("byte %d is held but reported missing", b)
					}
				}
			}
			var missingCount uint64
			for _, m := range s.Missing(length) {
				missingCount += m.Hi - m.Lo
			}
			if got := s.Received() + missingCount; got != length {
				t.Fatalf("received %d plus missing %d is %d, want %d",
					s.Received(), missingCount, got, length)
			}
		}
	}
}

func TestContiguousPrefixIgnoresARangePastAHole(t *testing.T) {
	s := NewIntervalSet()
	if err := s.Insert(10, 20); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// The bytes are on disk but the front is missing, so a resuming client
	// must be told zero rather than the part file's size.
	if got := s.ContiguousPrefix(); got != 0 {
		t.Fatalf("prefix over a hole = %d, want 0", got)
	}
	if err := s.Insert(0, 10); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if got := s.ContiguousPrefix(); got != 20 {
		t.Fatalf("prefix after the hole closed = %d, want 20", got)
	}
}

func TestZeroLengthUploadIsCompleteWhenEmpty(t *testing.T) {
	if !NewIntervalSet().IsComplete(0) {
		t.Fatal("an empty set does not report a zero-length upload complete")
	}
	if NewIntervalSet().IsComplete(1) {
		t.Fatal("an empty set reports a one-byte upload complete")
	}
	if got := FullIntervalSet(0).Count(); got != 0 {
		t.Fatalf("a full set of length zero holds %d runs, want 0", got)
	}
}

func TestInsertRefusesPastTheRunBoundAndLeavesTheSetAlone(t *testing.T) {
	s := NewIntervalSet()
	for i := 0; i < limits.UploadIntervalRuns; i++ {
		lo := uint64(i) * 10
		if err := s.Insert(lo, lo+1); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	before := s.Runs()
	over := uint64(limits.UploadIntervalRuns) * 10
	err := s.Insert(over, over+1)
	if !errors.Is(err, ErrFragmented) {
		t.Fatalf("the insert past the bound returned %v, want ErrFragmented", err)
	}
	if !slices.Equal(s.Runs(), before) {
		t.Fatal("a refused insert changed the set")
	}
	// A merging insert at the bound is still allowed: it does not grow the
	// count, and refusing it would strand a session that is filling its holes.
	if err := s.Insert(0, 25); err != nil {
		t.Fatalf("a merging insert at the bound was refused: %v", err)
	}
}

func TestLoadRebuildsTheNormalFormFromRows(t *testing.T) {
	// Rows arrive in whatever order the query returned them, and a pair that
	// touches must coalesce rather than be adopted as two runs.
	s, err := LoadIntervalSet([]Range{{10, 20}, {0, 10}, {30, 40}})
	if err != nil {
		t.Fatalf("LoadIntervalSet: %v", err)
	}
	if got, want := s.Runs(), []Range{{0, 20}, {30, 40}}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}

func TestLoadRefusesAnEmptyOrInvertedRow(t *testing.T) {
	for _, bad := range [][]Range{{{5, 5}}, {{9, 4}}} {
		if _, err := LoadIntervalSet(bad); err == nil {
			t.Fatalf("LoadIntervalSet(%v) was accepted", bad)
		}
	}
}

func buildSet(t *testing.T, ranges []Range) *IntervalSet {
	t.Helper()
	s := NewIntervalSet()
	for _, r := range ranges {
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			t.Fatalf("Insert(%d, %d): %v", r.Lo, r.Hi, err)
		}
	}
	return s
}

// coveredBytes is the set expanded one byte at a time, which is the slow and
// obviously correct model the fast implementation is checked against.
func coveredBytes(s *IntervalSet, length uint64) []bool {
	out := make([]bool, length)
	for _, r := range s.Runs() {
		for b := r.Lo; b < r.Hi && b < length; b++ {
			out[b] = true
		}
	}
	return out
}
