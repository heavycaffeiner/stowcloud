//go:build linux

package upload

import (
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// permutations is every ordering of a small slice, so a normal-form property
// is proved over all of them rather than over a random sample.
func permutations(in []Range) [][]Range {
	if len(in) <= 1 {
		return [][]Range{append([]Range(nil), in...)}
	}
	var out [][]Range
	for i := range in {
		rest := make([]Range, 0, len(in)-1)
		rest = append(rest, in[:i]...)
		rest = append(rest, in[i+1:]...)
		for _, tail := range permutations(rest) {
			out = append(out, append([]Range{in[i]}, tail...))
		}
	}
	return out
}

// The set has exactly one normal form however the ranges arrived, which is
// what makes the resumable offset an answer rather than an artefact of
// arrival order.
func TestTheSetHasOneNormalFormWhateverTheOrder(t *testing.T) {
	pieces := []Range{{0, 10}, {10, 20}, {25, 30}, {5, 15}, {28, 40}}
	want := []Range{{0, 20}, {25, 40}}

	// Every ordering rather than a sample of random ones: the set is five
	// ranges, so the whole permutation space is cheap and a failure names the
	// exact order that produced it.
	for _, shuffled := range permutations(pieces) {
		s := NewIntervalSet()
		for _, r := range shuffled {
			if err := s.Insert(r.Lo, r.Hi); err != nil {
				t.Fatalf("Insert(%d,%d): %v", r.Lo, r.Hi, err)
			}
		}
		got := s.Runs()
		if len(got) != len(want) {
			t.Fatalf("the set is %v, want %v (order %v)", got, want, shuffled)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("the set is %v, want %v (order %v)", got, want, shuffled)
			}
		}
	}
}

// Touching ranges merge as well as overlapping ones: two chunks that meet
// exactly are one contiguous region, and a set that kept them apart would
// report a resumable offset short of what actually landed.
func TestTouchingRangesMerge(t *testing.T) {
	s := NewIntervalSet()
	for _, r := range []Range{{0, 10}, {10, 20}, {20, 30}} {
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if s.Count() != 1 || s.ContiguousPrefix() != 30 {
		t.Fatalf("the set is %v with prefix %d", s.Runs(), s.ContiguousPrefix())
	}
}

func TestAnEmptyRangeChangesNothing(t *testing.T) {
	s := NewIntervalSet()
	if err := s.Insert(5, 5); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Insert(9, 3); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if s.Count() != 0 || s.Received() != 0 {
		t.Fatalf("the set is %v", s.Runs())
	}
}

// The refusal costs the client one chunk, not the session, so the set has to
// be exactly what it was before.
func TestTheRunBoundRefusesAndLeavesTheSetUnchanged(t *testing.T) {
	s := NewIntervalSet()
	// Disjoint runs, each separated by a gap, up to the bound.
	for i := 0; i < limits.UploadIntervalRuns; i++ {
		lo := uint64(i) * 10
		if err := s.Insert(lo, lo+5); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	before := s.Runs()

	far := uint64(limits.UploadIntervalRuns+10) * 10
	if err := s.Insert(far, far+5); !errors.Is(err, ErrFragmented) {
		t.Fatalf("an insert past the bound returned %v", err)
	}
	after := s.Runs()
	if len(after) != len(before) {
		t.Fatalf("the refused insert changed the set: %d runs, was %d", len(after), len(before))
	}

	// An insert that merges with what is there lowers the count and must not
	// be refused for being at the bound.
	if err := s.Insert(5, 10); err != nil {
		t.Fatalf("a merging insert at the bound returned %v", err)
	}
}

// Stored rows are inserted rather than adopted, so a set that would not
// rebuild is refused instead of becoming a hole the client resumes past.
func TestLoadRederivesTheInvariantAndRefusesCorruption(t *testing.T) {
	s, err := LoadIntervalSet([]Range{{20, 30}, {0, 10}, {5, 25}})
	if err != nil {
		t.Fatalf("LoadIntervalSet: %v", err)
	}
	if runs := s.Runs(); len(runs) != 1 || runs[0] != (Range{0, 30}) {
		t.Fatalf("unsorted overlapping rows loaded as %v", runs)
	}

	for _, bad := range [][]Range{
		{{5, 5}},
		{{9, 3}},
		{{0, 10}, {30, 20}},
	} {
		if _, lerr := LoadIntervalSet(bad); lerr == nil {
			t.Fatalf("the corrupt rows %v loaded", bad)
		}
	}
}

func TestPrefixCompleteMissingAndReceived(t *testing.T) {
	s := NewIntervalSet()
	for _, r := range []Range{{10, 20}, {30, 40}} {
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	// The prefix is zero because the set does not start at zero: a client
	// resuming has to send from the beginning.
	if s.ContiguousPrefix() != 0 {
		t.Fatalf("the prefix is %d, want 0 for a set that does not start at zero", s.ContiguousPrefix())
	}
	if s.Received() != 20 {
		t.Fatalf("received is %d, want 20", s.Received())
	}
	if s.IsComplete(40) {
		t.Fatal("a set with holes reported itself complete")
	}
	missing := s.Missing(40)
	want := []Range{{0, 10}, {20, 30}}
	if len(missing) != len(want) {
		t.Fatalf("missing is %v, want %v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("missing is %v, want %v", missing, want)
		}
	}

	if err := s.Insert(0, 10); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Insert(20, 30); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !s.IsComplete(40) || s.ContiguousPrefix() != 40 {
		t.Fatalf("the filled set is %v", s.Runs())
	}
	if len(s.Missing(40)) != 0 {
		t.Fatalf("a complete set reports %v missing", s.Missing(40))
	}
}

// A zero-length file is complete when empty: nothing arriving is the file
// having arrived, and it is the one case where that is true.
func TestAZeroLengthFileIsCompleteWhenEmpty(t *testing.T) {
	if !NewIntervalSet().IsComplete(0) {
		t.Fatal("an empty set is not complete for a zero-length file")
	}
	if NewIntervalSet().IsComplete(1) {
		t.Fatal("an empty set is complete for a file with a byte in it")
	}
	if FullIntervalSet(0).Count() != 0 {
		t.Fatal("the full set of a zero-length file holds a run")
	}
}

// A set that covers more than the declared length is still complete: a
// truncation elsewhere must not make a finished upload look unfinished.
func TestASetPastTheLengthIsStillComplete(t *testing.T) {
	s := FullIntervalSet(100)
	if !s.IsComplete(50) {
		t.Fatal("a set covering more than the length reported incomplete")
	}
}
