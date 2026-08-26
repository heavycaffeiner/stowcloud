package store

import "time"

// The size guard's vocabulary, held apart from the sampling because the
// settings document names these types and is built for every target, while
// the sampler needs statfs and is Linux only.

// GuardConfig is what the guard is asked for.
type GuardConfig struct {
	// MinFreeBytes trips the guard when the volume holding the databases has
	// less than this available. Zero is off, which is the default.
	MinFreeBytes uint64
	// MaxBytes trips it when the three databases together exceed this. Zero is
	// off. It is a bound on this server's own footprint, where MinFreeBytes is
	// a bound on the volume it shares with everything else.
	MaxBytes uint64
	// Interval is how often the volume is sampled. Zero takes a default.
	Interval time.Duration
}

// Enabled reports whether either bound is set.
func (g GuardConfig) Enabled() bool { return g.MinFreeBytes > 0 || g.MaxBytes > 0 }

// defaultGuardInterval is a compromise: often enough that a volume filling
// during a large upload is noticed before the writes start failing with
// ENOSPC, rare enough that it is not a statfs per second forever.
const defaultGuardInterval = 30 * time.Second

// GuardState is one sample, for the caller that reports health.
type GuardState struct {
	// Blocked is whether writes are refused right now.
	Blocked bool
	// AvailableBytes is what the volume had at the last sample.
	AvailableBytes uint64
	// StoreBytes is what the three databases occupied at the last sample.
	StoreBytes uint64
	// Reason names which bound tripped, empty when neither did.
	Reason string
}
