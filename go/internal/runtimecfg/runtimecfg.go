// Package runtimecfg is the settings an administrator moves from the web
// interface, held live and persisted so they survive a restart.
//
// The interface is where a deployment is configured, and it is the only place:
// there is no config file. The compiled-in defaults are the floor, the stored
// document is the only thing over them, and what the screen saves is what the
// server runs with from then on and after the next start.
//
// The one thing that is not stored here is where the store itself is. A
// process has to know that before it can read anything, so the data directory
// is a process argument and everything else is in the database.
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

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
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

	// WatchFullThreshold is how many directories may sit dirty before the
	// watcher invalidates whole shares instead. Taken when the watcher starts,
	// like the hot-set bound beside it.
	WatchFullThreshold int

	// The network boundary. Every one of these is read when the listener and
	// the guards are built, so a change is stored and applies on the next
	// start: the app and content host lists and the proxy ranges are the
	// exception, because their holders are live and the guard reads them per
	// request.
	AppHosts      []string
	TrustedProxy  []string
	HomesEnabled  bool
	HomesRoot     string
	SMB           SMB
	SMBConfigured bool

	// Listen is the bind address, always TLS. Read when the listener is built.
	Listen string

	// Hardening is what the operator asks of the sandbox. The shipped default
	// refuses to start when a layer cannot be applied.
	Hardening jail.Policy

	// DBGuard bounds the store's disk use. Off unless asked for: an instance
	// that stops accepting writes because a cache grew is worse than one that
	// uses more disk than expected.
	DBGuard store.GuardConfig

	// OIDC is the single-sign-on client, or nil when none is configured. Link
	// only: the provider authenticates and never creates an account here.
	//
	// The client secret is not in this struct. It rests in the store sealed
	// under the master key, and the wiring opens it after the key is available:
	// a settings document that carried it would put a credential in every read
	// of the settings table.
	OIDC            *OIDC
	OIDCDisplayName string
}

// OIDC is the provider settings as an administrator moves them, without the
// secret.
type OIDC struct {
	Enabled               bool
	Issuer                string
	ClientID              string
	Scopes                []string
	AllowPrivateEndpoints bool
	CACertFile            string
}

// SMB is the sidecar's settings as an administrator moves them.
//
// Held whole rather than field by field because the render is whole: the
// publisher takes a configuration and produces four files, so a half-applied
// change is a configuration nobody wrote.
type SMB struct {
	Enabled         bool
	Workgroup       string
	ServerName      string
	ServiceUser     string
	AllowPublicBind bool
	// TOTPPolicy is "require_separate" or "block". A string because it is the
	// client's own vocabulary and the auth package's enum is not.
	TOTPPolicy string
	ServiceGID uint32
	// Interfaces are what the sidecar binds. Empty means every private range
	// it can see, which is what a container that only sees a bridge gets.
	Interfaces []string
	// ConfigDir is the directory both containers mount, where the rendered
	// files go. AgentSocket is where the sidecar listens; empty means no
	// sidecar, which is a bare-metal deployment where something else applies
	// the files.
	ConfigDir   string
	AgentSocket string
}

// The compiled-in values a deployment starts as. With no config file these
// are the whole floor: a key nobody has saved runs as one of these.
const (
	// DefaultListen binds every interface. A first boot has no host list yet,
	// so a server that bound loopback would be one nobody on the LAN could
	// reach to configure it.
	DefaultListen = "0.0.0.0:8443"

	defaultRatePerSec = 20
	defaultRateBurst  = 100

	// DefaultSMBConfigDir is the directory both containers mount.
	DefaultSMBConfigDir = "/config/smb"
	// DefaultSMBServiceGID is the group Dockerfile.smb creates. The two have
	// to agree, and the agent says so loudly when they do not: it refuses the
	// sync with "no group exists for N" rather than syncing accounts nothing
	// can resolve.
	DefaultSMBServiceGID = 1000
	// DefaultSMBServiceUser is the account Dockerfile.smb creates, the same
	// pairing as the GID above.
	DefaultSMBServiceUser = "scsvc"
	defaultSMBWorkgroup   = "WORKGROUP"

	// defaultWatchHotSet matches watch.Config's own default. The watcher is
	// below this package in the graph and takes its config as a value, so this
	// is the number the wiring hands it rather than a second source of truth.
	defaultWatchHotSet = 4096
)

// Defaults are the compiled-in values, which are also the outer bounds' anchor.
func Defaults() Values {
	return Values{
		SearchConcurrentSSD:  limits.ConcurrentSearchesSSD,
		SearchConcurrentRot:  limits.ConcurrentSearchesRotational,
		SearchDeadlineSSD:    limits.SearchWalkDeadlineSSD,
		SearchDeadlineRot:    limits.SearchWalkDeadlineRotational,
		ArchiveMaxConcurrent: limits.ArchiveEntriesListed,
		WatchHotSetMax:       defaultWatchHotSet,
		RatePerSec:           defaultRatePerSec,
		RateBurst:            defaultRateBurst,
		Listen:               DefaultListen,
		Hardening:            jail.Required,
		SMB: SMB{
			Workgroup:   defaultSMBWorkgroup,
			ServiceUser: DefaultSMBServiceUser,
			ServiceGID:  DefaultSMBServiceGID,
			ConfigDir:   DefaultSMBConfigDir,
			TOTPPolicy:  "require_separate",
		},
	}
}

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

// BoundWatchFullThreshold is where the watcher stops enumerating dirty
// directories one at a time. It only fires above the hot-set bound, which is
// why its floor is that bound's floor rather than one.
func BoundWatchFullThreshold() Bound { return Bound{Min: 64, Max: 10 << 20} }

// BoundServiceGID excludes zero: that is root's group, the agent runs as root,
// and an account file putting every SMB account in it would be applied rather
// than questioned.
func BoundServiceGID() Bound { return Bound{Min: 1, Max: 1<<32 - 1} }

// Holds the live values. Every reader goes through it, so a change reaches
// every subsystem that asks rather than the ones somebody remembered.
type Holder struct {
	mu  sync.RWMutex
	val Values

	// apply pushes the values into the live components. It is set by the
	// wiring, which is the only layer that knows what those are.
	apply func(Values)
}

// New builds a holder over the values the server started with.
func New(v Values) *Holder { return &Holder{val: v} }

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

// Load reads the stored settings over the compiled-in defaults and returns
// what the server should run with.
//
// base is where a key nobody has saved comes from, which is Defaults for every
// caller in the product. A stored value outside its bound is clamped with a
// line in the log: refusing here would make a server unbootable over something
// saved weeks ago, and silently taking it would defeat the bound.
func Load(ctx context.Context, st Store, base Values, log *slog.Logger) Values {
	if log == nil {
		log = slog.Default()
	}
	all, err := st.Settings(ctx)
	if err != nil {
		log.Warn("the stored settings could not be read; running with the compiled-in defaults", "error", err)
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
	readInt(all, "watch", "full_threshold", BoundWatchFullThreshold(), log, func(v int64) {
		out.WatchFullThreshold = int(v)
	})

	// The network boundary. Stored as lists, and an entry that cannot be
	// parsed is dropped at boot with a line rather than refusing the start:
	// the same value is refused at save time, where somebody is watching.
	readStrings(all, "network", "app_hosts", func(v []string) { out.AppHosts = v })
	readStrings(all, "network", "trusted_proxies", func(v []string) { out.TrustedProxy = v })

	readBool(all, "homes", "enabled", func(v bool) { out.HomesEnabled = v })
	readString(all, "homes", "root", func(v string) { out.HomesRoot = v })

	// The listener. Dropped rather than clamped when it will not parse: an
	// address nothing can bind is a server that does not come up, and the same
	// value is refused at save time where somebody is watching.
	readString(all, "network", "bind", func(v string) {
		if err := CheckListen(v); err != nil {
			log.Warn("the stored bind address will not parse; using the default",
				"stored", v, "using", out.Listen, "error", err)
			return
		}
		out.Listen = v
	})

	readString(all, "security", "hardening", func(v string) {
		pol, perr := jail.ParsePolicy(v)
		if perr != nil {
			log.Warn("the stored hardening policy is not one this build has; using the default",
				"stored", v, "error", perr)
			return
		}
		out.Hardening = pol
	})

	// The size guard. The switch is what applies the bounds, so the numbers can
	// be set before it is turned on.
	if on, ok := boolAt(all, "db", "size_guard"); ok && on {
		readUint(all, "db", "max_bytes", func(v uint64) { out.DBGuard.MaxBytes = v })
		readUint(all, "db", "min_free_bytes", func(v uint64) { out.DBGuard.MinFreeBytes = v })
		if !out.DBGuard.Enabled() {
			log.Warn("the store's size guard is on and neither bound is set, so nothing would trip it; leaving it off")
		}
	}

	loadOIDC(all, &out, log)

	// SMB is read whole: a render takes a configuration and produces four
	// files, so a half-applied change is a configuration nobody wrote.
	if _, ok := all["smb"].(map[string]any); ok {
		out.SMBConfigured = true
		readBool(all, "smb", "enabled", func(v bool) { out.SMB.Enabled = v })
		readString(all, "smb", "workgroup", func(v string) { out.SMB.Workgroup = v })
		readString(all, "smb", "server_name", func(v string) { out.SMB.ServerName = v })
		readString(all, "smb", "service_user", func(v string) { out.SMB.ServiceUser = v })
		readBool(all, "smb", "allow_public_bind", func(v bool) { out.SMB.AllowPublicBind = v })
		readString(all, "smb", "totp_policy", func(v string) { out.SMB.TOTPPolicy = v })
		readStrings(all, "smb", "interfaces", func(v []string) { out.SMB.Interfaces = v })
		readString(all, "smb", "config_dir", func(v string) { out.SMB.ConfigDir = v })
		readString(all, "smb", "agent_socket", func(v string) { out.SMB.AgentSocket = v })
		readInt(all, "smb", "service_gid", BoundServiceGID(), log, func(v int64) {
			out.SMB.ServiceGID = uint32(v) //nolint:gosec // the bound above is what makes this fit.
		})
		// A rendered configuration nothing can produce is dropped rather than
		// carried to the first publish: SMB stays off and the log says why,
		// which is a deployment with no shares over SMB instead of one that
		// will not start.
		if out.SMB.Enabled {
			if err := CheckSMBRender(out.SMB); err != nil {
				log.Error("the stored SMB settings cannot be rendered; SMB stays off", "error", err)
				out.SMB.Enabled = false
			}
		}
	}
	return out
}

// loadOIDC reads the provider settings. Nil rather than a disabled client,
// because a client that exists and refuses is one every caller has to remember
// to ask about.
//
// Incomplete is off with a line, not a refused start: single sign-on missing
// is a deployment where people use passwords, and a server that will not boot
// is a deployment where nobody signs in at all.
func loadOIDC(all map[string]any, out *Values, log *slog.Logger) {
	on, ok := boolAt(all, "oidc", "enabled")
	if !ok || !on {
		return
	}
	var c OIDC
	c.Enabled = true
	readString(all, "oidc", "issuer", func(v string) { c.Issuer = v })
	readString(all, "oidc", "client_id", func(v string) { c.ClientID = v })
	readString(all, "oidc", "ca_cert_file", func(v string) { c.CACertFile = v })
	readStrings(all, "oidc", "scopes", func(v []string) { c.Scopes = v })
	readBool(all, "oidc", "allow_private_endpoints", func(v bool) { c.AllowPrivateEndpoints = v })
	if c.Issuer == "" || c.ClientID == "" {
		log.Error("single sign-on is on and has no issuer or client id; it stays off")
		return
	}
	out.OIDC = &c
	out.OIDCDisplayName = "Single sign-on"
	readString(all, "oidc", "display_name", func(v string) { out.OIDCDisplayName = v })
}

// readStrings pulls one stored list. A member of the wrong shape is dropped
// rather than taking the list down with it.
func readStrings(all map[string]any, section, key string, set func([]string)) {
	sec, ok := all[section].(map[string]any)
	if !ok {
		return
	}
	raw, ok := sec[key].([]any)
	if !ok {
		return
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	// An empty list is not applied: every one of these is a boundary, and a
	// stored empty would silently widen or close it depending on which.
	if len(out) > 0 {
		set(out)
	}
}

func readBool(all map[string]any, section, key string, set func(bool)) {
	if v, ok := boolAt(all, section, key); ok {
		set(v)
	}
}

// boolAt is readBool for a caller that has to branch on the value rather than
// hand it somewhere.
func boolAt(all map[string]any, section, key string) (bool, bool) {
	sec, ok := all[section].(map[string]any)
	if !ok {
		return false, false
	}
	v, ok := sec[key].(bool)
	return v, ok
}

// readUint pulls one stored byte count. Negative and fractional are dropped:
// a size is neither.
func readUint(all map[string]any, section, key string, set func(uint64)) {
	sec, ok := all[section].(map[string]any)
	if !ok {
		return
	}
	raw, ok := sec[key].(float64)
	if !ok || raw < 0 {
		return
	}
	set(uint64(raw))
}

func readString(all map[string]any, section, key string, set func(string)) {
	sec, ok := all[section].(map[string]any)
	if !ok {
		return
	}
	if v, ok := sec[key].(string); ok && v != "" {
		set(v)
	}
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
