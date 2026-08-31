// Package clock is the sole caller of time.Now in the engine tree. Code that
// needs the current time takes a Clock instead, so tests can hold time still
// and a clock with no working RTC gets one place to be handled sanely.
package clock

import (
	"log/slog"
	"sync"
	"time"
)

// Clock supplies time to anything above this package.
type Clock interface {
	// Now is the wall clock reading, suitable for anything persisted or sent
	// over the wire.
	Now() time.Time

	// Since returns monotonic elapsed time against t. Prefer this over
	// subtracting two Now() values: a wall clock can jump when NTP steps it,
	// a monotonic reading cannot.
	Since(t time.Time) time.Duration

	// Nanos is Now expressed as nanoseconds since the Unix epoch. A reading
	// before the epoch is clamped to zero rather than propagated as a
	// negative number or treated as fatal.
	Nanos() int64
}

// System returns a Clock backed by the real wall clock.
func System() Clock {
	return &systemClock{}
}

type systemClock struct {
	warnPreEpoch sync.Once
}

func (c *systemClock) Now() time.Time                  { return time.Now() }
func (c *systemClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (c *systemClock) Nanos() int64                    { return nanosSinceEpoch(time.Now(), &c.warnPreEpoch) }

// Fixed returns a Clock stuck at t. Since measures elapsed time against that
// same fixed instant, which keeps a test's duration arithmetic sensible even
// though Now never advances.
func Fixed(t time.Time) Clock {
	return &fixedClock{at: t}
}

type fixedClock struct {
	at           time.Time
	warnPreEpoch sync.Once
}

func (c *fixedClock) Now() time.Time                  { return c.at }
func (c *fixedClock) Since(t time.Time) time.Duration { return c.at.Sub(t) }
func (c *fixedClock) Nanos() int64                    { return nanosSinceEpoch(c.at, &c.warnPreEpoch) }

// nanosSinceEpoch converts t to nanoseconds since the epoch, clamping a
// pre-epoch reading to zero. A hardware clock that forgot the date is a
// condition to tolerate, not one to crash on, so the warning fires once per
// clock instance instead of flooding the log on every call.
func nanosSinceEpoch(t time.Time, warnOnce *sync.Once) int64 {
	if t.Before(time.Unix(0, 0)) {
		warnOnce.Do(func() {
			slog.Warn("clock reads before the Unix epoch, clamping to zero",
				slog.Time("read", t))
		})
		return 0
	}
	return t.UnixNano()
}
