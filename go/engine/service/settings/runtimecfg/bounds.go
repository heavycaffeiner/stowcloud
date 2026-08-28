package runtimecfg

import "fmt"

// Bound is what one field accepts, which is what the screen draws its validator
// from and what a save is checked against.
type Bound struct {
	Min int64
	Max int64
}

// Contains reports whether v is inside the bound.
func (b Bound) Contains(v int64) bool { return v >= b.Min && v <= b.Max }

// Clamp brings a value inside the bound, which is what boot time does with a
// stored value that has drifted outside it.
func (b Bound) Clamp(v int64) int64 {
	return min(max(v, b.Min), b.Max)
}

// The bounds. Each is the compiled-in limit's own range: an administrator moves
// a value inside it and a request path moves none of them.
//
// Functions rather than package variables, because a bound somebody can assign
// to is not a bound. The compiler inlines them.
func BoundSearchConcurrent() Bound { return Bound{Min: 1, Max: 64} }
func BoundSearchDeadlineMs() Bound { return Bound{Min: 100, Max: 60_000} }
func BoundArchiveEntries() Bound   { return Bound{Min: 100, Max: 1_000_000} }
func BoundWatchHotSet() Bound      { return Bound{Min: 64, Max: 1 << 20} }
func BoundRatePerSec() Bound       { return Bound{Min: 1, Max: 100_000} }
func BoundRateBurst() Bound        { return Bound{Min: 1, Max: 1_000_000} }

// BoundWatchFullThreshold is the point at which the watcher abandons per
// directory tracking. It can only take effect above the hot-set bound, so it
// shares that bound's floor instead of starting at one.
func BoundWatchFullThreshold() Bound { return Bound{Min: 64, Max: 10 << 20} }

// BoundServiceGID starts at one. Zero is root's group; since the agent runs as
// root, an account file assigning every SMB account to it would be written out
// and honoured rather than rejected.
func BoundServiceGID() Bound { return Bound{Min: 1, Max: 1<<32 - 1} }

// The settings paths, declared in one place. The screen, the loader and the
// checker each refer to the same field, and a typo in any separate copy yields
// a setting that stores successfully and is never read back.
const (
	FieldSearchConcurrentFast = "search.max_concurrent_fast"
	FieldSearchConcurrentSlow = "search.max_concurrent_slow"
	FieldSearchDeadlineFast   = "search.walk_deadline_fast_ms"
	FieldSearchDeadlineSlow   = "search.walk_deadline_slow_ms"
	FieldArchiveMaxConcurrent = "archive.max_concurrent"
	FieldWatchHotSet          = "watch.hot_set_max"
	FieldWatchFullThreshold   = "watch.full_threshold"
	FieldRatePerSec           = "rate.per_sec"
	FieldRateBurst            = "rate.burst"
	FieldSMBServiceGID        = "smb.service_gid"
)

// Bounds is every numeric field and its range, which the settings screen
// renders. The client never compiles its own copy: a client carrying its own
// bounds offers values the server refuses.
//
// A numeric field absent from this table is a field the screen cannot validate
// and the checker cannot refuse, so the table is the contract rather than a
// convenience.
func Bounds() map[string]Bound {
	return map[string]Bound{
		FieldSearchConcurrentFast: BoundSearchConcurrent(),
		FieldSearchConcurrentSlow: BoundSearchConcurrent(),
		FieldSearchDeadlineFast:   BoundSearchDeadlineMs(),
		FieldSearchDeadlineSlow:   BoundSearchDeadlineMs(),
		FieldArchiveMaxConcurrent: BoundArchiveEntries(),
		FieldWatchHotSet:          BoundWatchHotSet(),
		FieldWatchFullThreshold:   BoundWatchFullThreshold(),
		FieldRatePerSec:           BoundRatePerSec(),
		FieldRateBurst:            BoundRateBurst(),
		FieldSMBServiceGID:        BoundServiceGID(),
	}
}

// Check validates a single value during a save, while an administrator is
// present to react.
//
// The value is rejected by name rather than quietly adjusted, because a save
// that silently stores a different number can only be discovered by reading the
// setting back afterwards.
func Check(field string, v int64, b Bound) error {
	if !b.Contains(v) {
		return fmt.Errorf("%s must be between %d and %d", field, b.Min, b.Max)
	}
	return nil
}
