// Package clock is the only place in this tree that calls time.Now. Business
// logic takes a Clock so that a test can decide what time it is, and so that a
// wall clock behind the epoch is handled in one place instead of aborting the
// process wherever it is first noticed.
package clock

import (
	"log/slog"
	"sync"
	"time"
)

// Clock is the source of time for everything above this package.
type Clock interface {
	// Now is the wall clock. Use it for anything that gets stored or sent.
	Now() time.Time

	// Since is the monotonic elapsed time, which is what a duration wants:
	// a wall-clock subtraction moves when NTP steps the clock.
	Since(t time.Time) time.Duration

	// Nanos is Now as nanoseconds since the epoch, clamped at zero. An RTC
	// that lost its battery is an ordinary state, not a reason to die.
	Nanos() int64
}

// System returns the clock backed by time.Now.
func System() Clock { return &system{} }

type system struct{ behind sync.Once }

func (s *system) Now() time.Time                  { return time.Now() }
func (s *system) Since(t time.Time) time.Duration { return time.Since(t) }
func (s *system) Nanos() int64                    { return clamp(time.Now(), &s.behind) }

// Fixed returns a clock that always reports t. Since is measured against it, so
// a test can hold time still and still get sensible durations.
func Fixed(t time.Time) Clock { return &fixed{t: t} }

type fixed struct {
	t      time.Time
	behind sync.Once
}

func (f *fixed) Now() time.Time                  { return f.t }
func (f *fixed) Since(t time.Time) time.Duration { return f.t.Sub(t) }
func (f *fixed) Nanos() int64                    { return clamp(f.t, &f.behind) }

// clamp turns a wall-clock reading into nanoseconds since the epoch, reporting
// a clock behind the epoch once per clock rather than on every call, because a
// clock that is wrong is wrong for the whole run.
func clamp(t time.Time, once *sync.Once) int64 {
	if t.Before(time.Unix(0, 0)) {
		once.Do(func() {
			slog.Warn("wall clock is behind the epoch, timestamps are clamped to zero",
				slog.String("read", t.Format(time.RFC3339Nano)))
		})
		return 0
	}
	return t.UnixNano()
}
