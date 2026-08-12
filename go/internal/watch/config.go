// Package watch is change detection: a bounded set of watched directories, a
// debounce, and a periodic rescan behind them.
//
// Nothing here is a source of truth. Every read path re-stats before trusting
// anything, so a watcher that is degraded, capped or dead costs the freshness
// of a pushed update and never correctness. What the design promises is that a
// stale answer is always detectable and self-correcting, not that every change
// is seen immediately, which no container can promise: fs.inotify.
// max_user_watches cannot be raised from inside one, and a watch is per
// directory, so a large tree cannot be fully watched.
package watch

import (
	"fmt"
	"time"
)

// Backend names the change-detection transport. There is exactly one, and an
// unknown value is a configuration refusal rather than a warning and a
// fallback.
//
// A previous configuration accepted "fanotify", warned, and did something else.
// An operator who set it believed they had whole-mount watching, had per-
// directory watching, and the difference was invisible outside one startup log
// line. The value is gone until something implements it, and putting it back is
// a one-line addition on the day that stops being a lie.
type Backend uint8

const BackendInotify Backend = iota

func (b Backend) String() string { return "inotify" }

// ParseBackend is the trust boundary for the configured value.
func ParseBackend(s string) (Backend, error) {
	if s == "inotify" {
		return BackendInotify, nil
	}
	return 0, fmt.Errorf("watch backend %q is not a value this build implements; the only one is \"inotify\"", s)
}

// Config is the watcher's whole configuration.
type Config struct {
	Backend Backend

	// HotSetMax caps how many directories carry a live kernel watch at once.
	HotSetMax int

	// FullThreshold is how many directories may sit dirty before the watcher
	// stops enumerating them one at a time and invalidates whole shares
	// instead. It only fires for a deployment that raised HotSetMax past it,
	// which is the case it exists for.
	FullThreshold int

	// Debounce is how long a directory sits dirty before it is reported, so a
	// burst that finishes inside the window costs one event.
	Debounce time.Duration

	// RescanInterval is how often the directories of a share whose filesystem
	// loses events are re-marked dirty. Sixty seconds matches the staleness an
	// NFS client already tolerates on its own attribute cache, so this adds no
	// lag the mount had not already accepted.
	RescanInterval time.Duration
}

func DefaultConfig() Config {
	return Config{
		Backend:        BackendInotify,
		HotSetMax:      4096,
		FullThreshold:  50_000,
		Debounce:       200 * time.Millisecond,
		RescanInterval: 60 * time.Second,
	}
}

// withDefaults fills in anything a caller left at zero, so a partially
// configured watcher cannot spin at zero intervals.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.HotSetMax <= 0 {
		c.HotSetMax = d.HotSetMax
	}
	if c.FullThreshold <= 0 {
		c.FullThreshold = d.FullThreshold
	}
	if c.Debounce <= 0 {
		c.Debounce = d.Debounce
	}
	if c.RescanInterval <= 0 {
		c.RescanInterval = d.RescanInterval
	}
	return c
}
