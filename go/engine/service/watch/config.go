// Package watch provides change detection built from a capped set of watched
// directories, a debounce stage, and a periodic rescan behind them.
//
// None of it is authoritative. Read paths re-stat before trusting any of it, so
// a watcher that is capped, degraded or entirely dead costs only the promptness
// of a pushed update, never correctness. The guarantee is that stale answers
// remain detectable and self-correcting, not that every change is observed at
// once. No container could promise the latter: fs.inotify.max_user_watches is
// not raisable from inside one, and watches are per directory, so a large tree
// can never be covered completely.
//
// This is a sensor and not a writer. Consumers receive events and respond; the
// watcher never modifies the cache itself.
package watch

import (
	"fmt"
	"time"
)

// Backend identifies the change-detection transport. Only one exists, and an
// unrecognized value rejects the configuration instead of warning and falling
// back.
//
// An earlier configuration took "fanotify", logged a warning, and then ran
// something different. Operators who set it believed they had whole-mount
// watching while actually getting per-directory watching, a discrepancy visible
// only in a single startup log line. The name stays absent until an
// implementation exists; restoring it is a one-line change on the day it stops
// being false.
type Backend uint8

// BackendInotify is the only transport this build implements.
const BackendInotify Backend = iota

func (b Backend) String() string { return "inotify" }

// ParseBackend validates the configured value at the trust boundary.
func ParseBackend(s string) (Backend, error) {
	if s == "inotify" {
		return BackendInotify, nil
	}
	return 0, fmt.Errorf(
		"watch backend %q is not a value this build implements; the only one is %q", s, "inotify")
}

// Config carries every setting the watcher takes.
type Config struct {
	Backend Backend

	// HotSetMax limits how many directories may hold a live kernel watch
	// simultaneously.
	HotSetMax int

	// FullThreshold bounds how many directories may be dirty at once before the
	// watcher abandons per-directory enumeration in favour of invalidating
	// entire shares. It can only trigger where HotSetMax has been raised beyond
	// it, which is the situation it was written for.
	FullThreshold int

	// Debounce sets how long a directory stays dirty before being reported,
	// letting a burst that completes inside the window produce one event.
	Debounce time.Duration

	// RescanInterval controls how frequently directories belonging to shares on
	// event-losing filesystems are marked dirty again. Sixty seconds mirrors the
	// staleness an NFS client already accepts in its attribute cache, so this
	// introduces no delay the mount had not already tolerated.
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

// withDefaults substitutes values for any field left at zero, preventing a
// partially configured watcher from spinning on zero-length intervals.
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
