// Package runtimecfg is the settings an administrator moves from the web
// interface, held live and persisted so they survive a restart.
//
// The product's rule is that the interface is where a deployment is
// configured and the config file is the floor under it: sc.toml states what a
// deployment starts as, an administrator adjusts it from the screen, and the
// adjustment is what the server runs with from then on. Before this the second
// half was missing. A patch was written to the settings table, nothing read it
// back, and the response said "applied": the screen reported a change that had
// taken effect nowhere, and the next read showed the old value.
//
// Three rules hold everything here together.
//
// A compiled-in limit is the default and the outer bound. An administrator
// moves a value within it and no request path moves any of them, so a bound
// that exists to stop a caller widening it still does.
//
// A value is validated where somebody is watching. A patch outside the bound is
// refused naming the field, at save time, with an administrator looking at the
// screen. The same value arriving from the stored document at boot is clamped
// with a line in the log instead, because refusing there makes a server
// unbootable over a value saved weeks ago.
//
// Applying is separate from storing. A setting says whether it took effect,
// and the ones that cannot take effect until a restart say that rather than
// implying they are live.
package runtimecfg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// Values is the whole of what an administrator may move at runtime.
//
// One flat struct rather than a document per section: the sections are how the
// screen groups fields, and the server has no reason to hold ten shapes when
// what it needs is the values.
type Values struct {
	// The search tier's bounds. Every one of these is applied to the live
	// service; none of them needs a restart.
	SearchConcurrentSSD  int
	SearchConcurrentRot  int
	SearchDeadlineSSD    time.Duration
	SearchDeadlineRot    time.Duration
	ArchiveMaxConcurrent int

	// WatchHotSetMax is the watcher's hot-set bound.
	WatchHotSetMax int

	// The request rate bounds, which the limiter holds live.
	RatePerSec float64
	RateBurst  int
}

// Defaults are the compiled-in values, which are also the outer bounds' anchor.
func Defaults() Values {
	return Values{
		SearchConcurrentSSD:  limits.ConcurrentSearchesSSD,
		SearchConcurrentRot:  limits.ConcurrentSearchesRotational,
		SearchDeadlineSSD:    limits.SearchWalkDeadlineSSD,
		SearchDeadlineRot:    limits.SearchWalkDeadlineRotational,
		ArchiveMaxConcurrent: limits.ArchiveEntriesListed,
		// The watcher's own default, which its package owns. It is duplicated
		// nowhere: the wiring passes the live value into watch.Config.
		WatchHotSetMax: defaultWatchHotSet,
		RatePerSec:     0, // taken from the config file, which has no compiled-in default
		RateBurst:      0,
	}
}

// defaultWatchHotSet matches watch.Config's own default. The watcher is below
// this package in the graph and takes its config as a value, so this is the
// number the wiring hands it rather than a second source of truth.
const defaultWatchHotSet = 4096

// Bound is what one field accepts, which is what the screen draws its
// validator from and what a save is checked against.
type Bound struct {
	Min int64
	Max int64
}

// The bounds. Each is the compiled-in limit's own range: an administrator
// moves a value inside it and a request path moves none of them.
//
// Functions rather than package variables, because a bound somebody can
// assign to is not a bound. The compiler inlines them.
func BoundSearchConcurrent() Bound { return Bound{Min: 1, Max: 64} }
func BoundSearchDeadlineMs() Bound { return Bound{Min: 100, Max: 60_000} }
func BoundArchiveEntries() Bound   { return Bound{Min: 100, Max: 1_000_000} }
func BoundWatchHotSet() Bound      { return Bound{Min: 64, Max: 1 << 20} }
func BoundRatePerSec() Bound       { return Bound{Min: 1, Max: 100_000} }
func BoundRateBurst() Bound        { return Bound{Min: 1, Max: 1_000_000} }

// Holds the live values. Every reader goes through it, so a change reaches
// every subsystem that asks rather than the ones somebody remembered.
type Holder struct {
	mu  sync.RWMutex
	val Values
	// base is what the config file said, which is the floor a stored override
	// sits on and what a revert returns to. Held so a reload after a save does
	// not re-derive it from values an earlier save already moved.
	base Values

	// apply pushes the values into the live components. It is set by the
	// wiring, which is the only layer that knows what those are.
	apply func(Values)
}

// New builds a holder over what the config file said.
func New(base Values) *Holder { return &Holder{val: base, base: base} }

// Base is the config file's values, which a stored override sits on top of.
func (h *Holder) Base() Values {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.base
}

// Get is the live values.
func (h *Holder) Get() Values {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.val
}

// OnApply installs what pushes a change into the running components.
func (h *Holder) OnApply(fn func(Values)) {
	h.mu.Lock()
	h.apply = fn
	h.mu.Unlock()
}

// Set replaces the values and pushes them into the live components.
func (h *Holder) Set(v Values) {
	h.mu.Lock()
	h.val = v
	fn := h.apply
	h.mu.Unlock()
	if fn != nil {
		fn(v)
	}
}

// Store is the durable half, which this package does not implement: the
// settings table lives in the store and this only needs to read and write one
// document under one key.
type Store interface {
	Settings(ctx context.Context) (map[string]any, error)
	MergeSettings(ctx context.Context, section string, value any) error
}

// Load reads the stored overrides over a base and returns what the server
// should run with.
//
// base is what the config file said, so a key nobody has overridden keeps the
// operator's own value rather than the compiled-in one. A stored value outside
// its bound is clamped with a line in the log: refusing here would make a
// server unbootable over something saved weeks ago, and silently taking it
// would defeat the bound.
func Load(ctx context.Context, st Store, base Values, log *slog.Logger) Values {
	if log == nil {
		log = slog.Default()
	}
	all, err := st.Settings(ctx)
	if err != nil {
		log.Warn("the stored settings could not be read; running with the config file's values", "error", err)
		return base
	}

	out := base
	readInt(all, "search", "max_concurrent_fast", BoundSearchConcurrent(), log, func(v int64) {
		out.SearchConcurrentSSD = int(v)
	})
	readInt(all, "search", "max_concurrent_slow", BoundSearchConcurrent(), log, func(v int64) {
		out.SearchConcurrentRot = int(v)
	})
	readInt(all, "search", "walk_deadline_fast_ms", BoundSearchDeadlineMs(), log, func(v int64) {
		out.SearchDeadlineSSD = time.Duration(v) * time.Millisecond
	})
	readInt(all, "search", "walk_deadline_slow_ms", BoundSearchDeadlineMs(), log, func(v int64) {
		out.SearchDeadlineRot = time.Duration(v) * time.Millisecond
	})
	readInt(all, "archive", "max_concurrent", BoundArchiveEntries(), log, func(v int64) {
		out.ArchiveMaxConcurrent = int(v)
	})
	readInt(all, "watch", "hot_set_max", BoundWatchHotSet(), log, func(v int64) {
		out.WatchHotSetMax = int(v)
	})
	readInt(all, "rate", "per_sec", BoundRatePerSec(), log, func(v int64) {
		out.RatePerSec = float64(v)
	})
	readInt(all, "rate", "burst", BoundRateBurst(), log, func(v int64) {
		out.RateBurst = int(v)
	})
	return out
}

// readInt pulls one stored integer, clamps it and hands it over.
//
// Absent leaves the base value alone, which is what makes the config file the
// floor rather than something the first save erases.
func readInt(all map[string]any, section, key string, b Bound, log *slog.Logger, set func(int64)) {
	sec, ok := all[section].(map[string]any)
	if !ok {
		return
	}
	// JSON carries every number as a float. A value of any other shape is
	// ignored rather than guessed at.
	raw, ok := sec[key].(float64)
	if !ok {
		return
	}
	v := int64(raw)
	if c := clamp(v, b); c != v {
		log.Warn("a stored setting is outside its bound and was clamped",
			"setting", section+"."+key, "stored", v, "using", c,
			"min", b.Min, "max", b.Max)
		v = c
	}
	set(v)
}

func clamp(v int64, b Bound) int64 {
	if v < b.Min {
		return b.Min
	}
	if v > b.Max {
		return b.Max
	}
	return v
}

// Check validates one value at save time, where an administrator is watching.
//
// Refused rather than clamped here, and named: a save that silently became a
// different number is a setting somebody has to discover by reading it back.
func Check(field string, v int64, b Bound) error {
	if v < b.Min || v > b.Max {
		return fmt.Errorf("%s must be between %d and %d", field, b.Min, b.Max)
	}
	return nil
}
