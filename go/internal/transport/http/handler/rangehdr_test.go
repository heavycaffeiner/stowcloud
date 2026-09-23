// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"errors"
	"testing"
)

// No header is the ordinary whole-file request, not a problem.
func TestAnAbsentRangeIsNotAnError(t *testing.T) {
	for _, h := range []string{"", "   "} {
		_, ok, err := ParseRange(h, 1000)
		if ok || err != nil {
			t.Errorf("the header %q returned ok=%v err=%v", h, ok, err)
		}
	}
}

// The ordinary forms resolve to half-open intervals against the file's size.
func TestTheRangeFormsResolve(t *testing.T) {
	const size = 1000
	for _, c := range []struct {
		header     string
		start, end int64
	}{
		{"bytes=0-99", 0, 100},
		{"bytes=100-199", 100, 200},
		{"bytes=999-999", 999, 1000},
		{"bytes=500-", 500, 1000},
		{"bytes=-100", 900, 1000},
		// A suffix longer than the file is the whole file: the client asked
		// for at most that many from the end.
		{"bytes=-5000", 0, 1000},
		// An end past the file is clamped, so a client with a stale size gets
		// what exists rather than a refusal.
		{"bytes=900-5000", 900, 1000},
		{"bytes= 0 - 99 ", 0, 100},
	} {
		got, ok, err := ParseRange(c.header, size)
		if err != nil || !ok {
			t.Errorf("%q returned ok=%v err=%v", c.header, ok, err)
			continue
		}
		if got.Start != c.start || got.End != c.end {
			t.Errorf("%q resolved to [%d,%d), want [%d,%d)", c.header, got.Start, got.End, c.start, c.end)
		}
	}
}

// A multi-range request is refused rather than served as its first range. A
// client that asked for three pieces and got one, with a 206 saying nothing
// went wrong, assembles a broken file and finds out later.
func TestAMultiRangeRequestIsRefusedNotReduced(t *testing.T) {
	got, ok, err := ParseRange("bytes=0-99,200-299", 1000)
	if !errors.Is(err, ErrRangeUnsupported) {
		t.Fatalf("a multi-range request returned %+v ok=%v err=%v", got, ok, err)
	}
	if ok {
		t.Error("a multi-range request was reported as satisfiable")
	}
}

// Another unit is refused rather than ignored. Ignoring it would send the
// whole file with a 200, which the client would treat as the part it asked
// for.
func TestAnotherUnitIsRefused(t *testing.T) {
	for _, h := range []string{"items=0-9", "0-99", "bytes 0-99"} {
		if _, _, err := ParseRange(h, 1000); !errors.Is(err, ErrRangeUnsupported) {
			t.Errorf("%q returned %v", h, err)
		}
	}
}

// A range past the end is unsatisfiable rather than unsupported: the syntax
// was fine and the file is simply shorter, which is what a 416 with the real
// size tells the client.
func TestARangePastTheEndIsUnsatisfiable(t *testing.T) {
	_, _, err := ParseRange("bytes=1000-1099", 1000)
	if !errors.Is(err, ErrRangeUnsatisfiable) {
		t.Fatalf("a range past the end returned %v", err)
	}
	if errors.Is(err, ErrRangeUnsupported) {
		t.Error("a range past the end was also reported as unsupported")
	}

	// The two are distinct answers, and a caller renders them as different
	// statuses.
	if _, _, uerr := ParseRange("bytes=abc-def", 1000); errors.Is(uerr, ErrRangeUnsatisfiable) {
		t.Error("a malformed range was reported as unsatisfiable")
	}
}

// Malformed input is refused rather than coerced into some range.
func TestMalformedRangesAreRefused(t *testing.T) {
	for _, h := range []string{
		"bytes=",
		"bytes=-",
		"bytes=abc-def",
		"bytes=10",
		"bytes=-abc",
		"bytes=--5",
		"bytes=99-10",
		"bytes=-0",
	} {
		if _, ok, err := ParseRange(h, 1000); err == nil || ok {
			t.Errorf("%q was accepted as ok=%v err=%v", h, ok, err)
		}
	}
}

// The headers say what the range covers, in the inclusive form the wire uses.
func TestTheContentRangeHeaders(t *testing.T) {
	r, _, err := ParseRange("bytes=100-199", 1000)
	if err != nil {
		t.Fatalf("ParseRange: %v", err)
	}
	if r.Length() != 100 {
		t.Errorf("the length is %d", r.Length())
	}
	// Exclusive internally, inclusive on the wire: 100-199, not 100-200.
	if got := r.ContentRange(1000); got != "bytes 100-199/1000" {
		t.Errorf("the header is %q", got)
	}
	if got := UnsatisfiedRange(1000); got != "bytes */1000" {
		t.Errorf("the unsatisfied header is %q", got)
	}
}

// An empty file has no satisfiable range, and every form says so rather than
// producing an interval that reads past its end.
func TestAnEmptyFileHasNoSatisfiableRange(t *testing.T) {
	for _, h := range []string{"bytes=0-0", "bytes=0-", "bytes=-1"} {
		got, ok, err := ParseRange(h, 0)
		if err == nil {
			t.Errorf("%q against an empty file returned [%d,%d) ok=%v", h, got.Start, got.End, ok)
		}
	}
}
