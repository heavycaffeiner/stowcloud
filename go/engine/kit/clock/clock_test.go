package clock

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestFixedNowStaysPut(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := Fixed(at)
	if got := c.Now(); !got.Equal(at) {
		t.Fatalf("Now() = %v, want %v", got, at)
	}
	if got := c.Now(); !got.Equal(at) {
		t.Fatalf("second Now() = %v, want %v (still fixed)", got, at)
	}
}

func TestFixedSinceMeasuresAgainstItsOwnInstant(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := Fixed(at)
	earlier := at.Add(-10 * time.Second)
	if got := c.Since(earlier); got != 10*time.Second {
		t.Fatalf("Since(earlier) = %v, want 10s", got)
	}
	later := at.Add(10 * time.Second)
	if got := c.Since(later); got != -10*time.Second {
		t.Fatalf("Since(later) = %v, want -10s", got)
	}
}

func TestNanosClampsPreEpochToZero(t *testing.T) {
	c := Fixed(time.Unix(-1000, 0))
	if got := c.Nanos(); got != 0 {
		t.Fatalf("Nanos() = %d, want 0 for a pre-epoch clock", got)
	}
}

func TestNanosPassesThroughAfterEpoch(t *testing.T) {
	at := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	c := Fixed(at)
	if got := c.Nanos(); got != at.UnixNano() {
		t.Fatalf("Nanos() = %d, want %d", got, at.UnixNano())
	}
}

// countingHandler counts how many log records pass through it, so the test
// can check the pre-epoch warning fires once per clock instance rather than
// once per Nanos call.
type countingHandler struct{ n *int }

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(context.Context, slog.Record) error {
	*h.n++
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

func TestNanosWarnsOncePerClockInstanceNotPerCall(t *testing.T) {
	var count int
	prev := slog.Default()
	slog.SetDefault(slog.New(countingHandler{n: &count}))
	defer slog.SetDefault(prev)

	c := Fixed(time.Unix(-5, 0))
	c.Nanos()
	c.Nanos()
	c.Nanos()

	if count != 1 {
		t.Fatalf("warning fired %d times across 3 calls, want exactly 1", count)
	}
}

func TestNanosWarnsSeparatelyPerInstance(t *testing.T) {
	var count int
	prev := slog.Default()
	slog.SetDefault(slog.New(countingHandler{n: &count}))
	defer slog.SetDefault(prev)

	a := Fixed(time.Unix(-5, 0))
	b := Fixed(time.Unix(-5, 0))
	a.Nanos()
	b.Nanos()

	if count != 2 {
		t.Fatalf("warning fired %d times for 2 distinct clocks, want 2", count)
	}
}

func TestSystemClockAdvancesAndStaysPositive(t *testing.T) {
	c := System()
	start := c.Now()
	time.Sleep(time.Millisecond)
	if elapsed := c.Since(start); elapsed <= 0 {
		t.Fatalf("Since(start) = %v, want positive elapsed time", elapsed)
	}
	if c.Nanos() <= 0 {
		t.Fatal("Nanos() on a real clock should be positive")
	}
}
