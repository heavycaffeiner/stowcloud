package transfer

import (
	"errors"
	"testing"
)

func TestIntervalSetNormalizesOrderAndTouchingRanges(t *testing.T) {
	t.Parallel()
	pieces := []Range{{25, 30}, {10, 20}, {0, 10}, {28, 40}, {5, 15}}
	s := NewIntervalSet()
	for _, r := range pieces {
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			t.Fatalf("Insert(%d,%d): %v", r.Lo, r.Hi, err)
		}
	}
	want := []Range{{0, 20}, {25, 40}}
	if got := s.Runs(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("runs = %v, want %v", got, want)
	}

	if err := s.Insert(20, 25); err != nil {
		t.Fatalf("touching Insert: %v", err)
	}
	if got := s.Runs(); len(got) != 1 || got[0] != (Range{0, 40}) {
		t.Fatalf("touching ranges remained separate: %v", got)
	}
}

func TestIntervalSetMaxRunsIsInjected(t *testing.T) {
	t.Parallel()
	s := NewIntervalSet(WithMaxRuns(2))
	for _, r := range []Range{{0, 5}, {10, 15}} {
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			t.Fatalf("Insert(%v): %v", r, err)
		}
	}
	before := s.Runs()
	if err := s.Insert(20, 25); !errors.Is(err, ErrFragmented) {
		t.Fatalf("past-bound insert error = %v, want ErrFragmented", err)
	}
	if got := s.Runs(); len(got) != len(before) || got[0] != before[0] || got[1] != before[1] {
		t.Fatalf("refused insert changed runs: %v, before %v", got, before)
	}
	if err := s.Insert(5, 10); err != nil {
		t.Fatalf("merging insert at bound: %v", err)
	}
	if got := s.Runs(); len(got) != 1 || got[0] != (Range{0, 15}) {
		t.Fatalf("merging insert did not reduce runs: %v", got)
	}
}

func TestIntervalSetQueriesPreserveHalfOpenSemantics(t *testing.T) {
	t.Parallel()
	s := NewIntervalSet()
	for _, r := range []Range{{10, 20}, {30, 40}} {
		if err := s.Insert(r.Lo, r.Hi); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.ContiguousPrefix(); got != 0 {
		t.Fatalf("prefix = %d, want 0", got)
	}
	if got := s.Received(); got != 20 {
		t.Fatalf("received = %d, want 20", got)
	}
	wantMissing := []Range{{0, 10}, {20, 30}}
	gotMissing := s.Missing(40)
	if len(gotMissing) != len(wantMissing) {
		t.Fatalf("missing = %v, want %v", gotMissing, wantMissing)
	}
	for i := range wantMissing {
		if gotMissing[i] != wantMissing[i] {
			t.Fatalf("missing = %v, want %v", gotMissing, wantMissing)
		}
	}
	if s.IsComplete(40) {
		t.Fatal("set with a hole reported complete")
	}
	if err := s.Insert(0, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.Insert(20, 30); err != nil {
		t.Fatal(err)
	}
	if !s.IsComplete(40) || s.ContiguousPrefix() != 40 || len(s.Missing(40)) != 0 {
		t.Fatalf("filled set has wrong queries: runs=%v prefix=%d missing=%v", s.Runs(), s.ContiguousPrefix(), s.Missing(40))
	}
}

func TestLoadIntervalSetRejectsInvalidRowsAndNormalizesValidRows(t *testing.T) {
	t.Parallel()
	s, err := LoadIntervalSet([]Range{{20, 30}, {0, 10}, {5, 25}}, WithMaxRuns(4))
	if err != nil {
		t.Fatalf("LoadIntervalSet: %v", err)
	}
	if got := s.Runs(); len(got) != 1 || got[0] != (Range{0, 30}) {
		t.Fatalf("loaded runs = %v", got)
	}
	for _, rows := range [][]Range{{{5, 5}}, {{9, 3}}, {{0, 10}, {30, 20}}} {
		if _, err := LoadIntervalSet(rows); err == nil {
			t.Fatalf("invalid rows %v loaded", rows)
		}
	}
}

func TestFullIntervalSetAndEmptyRanges(t *testing.T) {
	t.Parallel()
	if got := FullIntervalSet(0).Count(); got != 0 {
		t.Fatalf("empty full set count = %d", got)
	}
	if !FullIntervalSet(10).IsComplete(10) {
		t.Fatal("full set is incomplete")
	}
	s := NewIntervalSet()
	if err := s.Insert(5, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.Insert(9, 3); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 0 || s.Received() != 0 {
		t.Fatalf("empty inserts changed set: %v", s.Runs())
	}
}
