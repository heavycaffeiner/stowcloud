package clock

import (
	"testing"
	"time"
)

// The clock this replaces unwrapped duration_since(UNIX_EPOCH), which aborts
// the process on a machine whose RTC has not been set. That is an ordinary
// state after a dead battery, and the upload engine sits on the request path.
func TestNanosClampsAClockBehindTheEpoch(t *testing.T) {
	c := Fixed(time.Unix(0, 0).Add(-time.Hour))
	if got := c.Nanos(); got != 0 {
		t.Fatalf("Nanos = %d, want 0", got)
	}
}

func TestNanosPassesAClockAheadOfTheEpoch(t *testing.T) {
	want := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if got := Fixed(want).Nanos(); got != want.UnixNano() {
		t.Fatalf("Nanos = %d, want %d", got, want.UnixNano())
	}
}

func TestSystemClockAdvances(t *testing.T) {
	c := System()
	start := c.Now()
	if c.Since(start) < 0 {
		t.Fatal("Since reported a negative elapsed time")
	}
	if c.Nanos() <= 0 {
		t.Fatal("Nanos reported a clock at or behind the epoch")
	}
}
