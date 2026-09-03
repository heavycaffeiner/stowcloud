// Package runtimecfg holds the operator-adjustable settings, kept live in
// memory and written to the database so they outlast a restart.
//
// Configuration happens through the web interface and nowhere else; the
// deployment ships no configuration file. Compiled-in defaults form the base
// layer, the stored document is the single override on top, and whatever the
// screen writes governs the server immediately and across restarts.
//
// One value cannot live here: the location of the store itself. A process must
// know it before any read is possible, so the data directory arrives as a
// process argument while everything else comes from the database.
//
// Three rules shape the package.
//
// Each compiled-in limit serves as both the default and the outer bound.
// Administrators adjust within it, and no request path can adjust any of them,
// so a bound placed there to stop callers widening it continues to do so.
//
// Validation happens when a human is present. A patch outside the bound is
// rejected by field name at save time, with the administrator on the screen.
// The identical value read from the stored document during startup is clamped
// and logged instead. Rejecting at boot would strand the server on a value
// saved long ago, and the emergency door that could repair it writes to this
// very document.
//
// Storing and applying are reported separately. Each setting states whether it
// is in effect, and those that require a restart say so instead of appearing
// live.
package runtimecfg

import (
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
)

// Values is the complete set of runtime-adjustable settings.
//
// A single flat struct instead of one document per section. Sections exist to
// group fields on the screen; the server needs the values themselves and has no
// use for ten separate shapes.
type Values struct {
	// Search tier limits. Both reach the running service directly and
	// neither requires a restart.
	SearchConcurrentSSD  int
	SearchDeadlineSSD    time.Duration
	ArchiveMaxConcurrent int

	// WatchHotSetMax caps how many directories the watcher keeps hot.
	WatchHotSetMax int

	// Request rate limits, applied by the limiter without a restart.
	RatePerSec float64
	RateBurst  int

	// WatchFullThreshold is the count of dirty directories past which the
	// watcher gives up and invalidates entire shares. Read at watcher startup,
	// as with the hot-set cap above.
	WatchFullThreshold int

	// The network boundary. Most of these are read when the listener and the
	// guards are built, so a change is stored and applies on the next start.
	// The host lists and the proxy ranges are the exception: their holders are
	// live and the guard reads them per request.
	//
	// AppHosts carry the session application. ContentHosts are the cookie-free
	// origin compatibility content is served from, and the two sets must be
	// disjoint: one TLS name cannot be both.
	AppHosts     []string
	ContentHosts []string
	// AllowedOrigins govern explicitly CORS-readable public compatibility
	// responses only. They never widen the host guard and never confer
	// credential trust.
	AllowedOrigins []string
	// CompatCanonicalURL is the fallback used when a request origin is
	// unavailable. It has to be one of the declared app origins.
	CompatCanonicalURL string

	TrustedProxy  []string
	HomesEnabled  bool
	HomesRoot     string
	SMB           smb.Config
	SMBTOTPPolicy string
	SMBServiceGID uint32
	SMBConfigDir  string
	SMBSocket     string
	SMBConfigured bool

	// Listen is the TLS bind address, consulted while constructing the listener.
	Listen string

	// Hardening is the sandbox strictness the operator requests. Out of the box
	// the server declines to start if any layer fails to apply.
	Hardening jail.Policy

	// DBGuard limits how much disk the store consumes. Disabled unless
	// requested: refusing writes because a cache expanded is a worse outcome
	// than occupying more space than anticipated.
	DBGuard GuardConfig

	// OIDC configures the single-sign-on client, nil when unconfigured. It links
	// existing accounts only; the provider proves identity and never provisions
	// an account here.
	//
	// The client secret is deliberately absent. It sits in the store encrypted
	// under the master key and is unsealed by the wiring once that key exists.
	// Carrying it in the settings document would expose a credential on every
	// read of the settings table.
	OIDC            *OIDC
	OIDCDisplayName string

	// ThumbnailEnabled controls whether the preview service generates thumbnails.
	ThumbnailEnabled bool

	// ThumbnailDir overrides where thumbnails are stored. Empty uses <dataDir>/thumbs.
	ThumbnailDir string
}

// OIDC is the administrator-editable provider configuration, secret excluded.
type OIDC struct {
	Enabled               bool
	Issuer                string
	ClientID              string
	Scopes                []string
	AllowPrivateEndpoints bool
	CACertFile            string
}

// GuardConfig bounds the store's disk use.
//
// It lives here rather than in the store because this is the package that
// configures it: dbfile owns the flag and the check, and the sampler that
// decides the flag's value takes one of these.
type GuardConfig struct {
	// MinFreeBytes fires the guard once the volume holding the databases drops
	// below this much free space. Zero disables it, and is the default.
	MinFreeBytes uint64
	// MaxBytes trips it when the databases together exceed this. Zero is off.
	// It is a bound on this server's own footprint, where MinFreeBytes is a
	// bound on the volume it shares with everything else.
	MaxBytes uint64
	// Interval sets the sampling period for the volume. Zero selects a default.
	Interval time.Duration
}

// Enabled reports whether at least one limit is configured.
func (g GuardConfig) Enabled() bool { return g.MinFreeBytes > 0 || g.MaxBytes > 0 }

// The compiled-in values a deployment starts as. With no config file these are
// the whole floor: a key nobody has saved runs as one of these.
const (
	// DefaultListen covers every interface. On a first boot no host list exists,
	// and binding loopback would leave the server unreachable from the LAN it
	// needs to be configured from.
	DefaultListen = "0.0.0.0:8443"

	defaultRatePerSec = 20
	defaultRateBurst  = 100

	// DefaultSMBConfigDir is the mount point shared by both containers.
	DefaultSMBConfigDir = "/config/smb"
	// DefaultSMBServiceGID is the group Dockerfile.smb creates. The two have to
	// agree, and the agent says so loudly when they do not: it refuses the sync
	// with "no group exists for N" rather than syncing accounts nothing can
	// resolve.
	DefaultSMBServiceGID = 1000

	// DefaultSMBTOTPPolicy is how an SMB account interacts with a second
	// factor. A string because it is the client's own vocabulary.
	DefaultSMBTOTPPolicy = "require_separate"

	// defaultWatchHotSet mirrors the watcher's own default. The watcher sits
	// lower in the dependency graph and receives its configuration by value, so
	// this constant is what the wiring passes in, not a competing definition.
	defaultWatchHotSet = 4096
)

// Defaults returns the compiled-in values, which also anchor the outer bounds.
//
// The SMB defaults come from the smb package rather than being spelled again
// here: two copies of a default are two values that drift.
func Defaults() Values {
	return Values{
		SearchConcurrentSSD:  limits.ConcurrentSearches,
		SearchDeadlineSSD:    limits.SearchWalkDeadline,
		ArchiveMaxConcurrent: limits.ArchiveEntriesListed,
		WatchHotSetMax:       defaultWatchHotSet,
		RatePerSec:           defaultRatePerSec,
		RateBurst:            defaultRateBurst,
		Listen:               DefaultListen,
		Hardening:            jail.Required,
		SMB:                  smb.Config{}.WithDefaults(),
		SMBTOTPPolicy:        DefaultSMBTOTPPolicy,
		SMBServiceGID:        DefaultSMBServiceGID,
		SMBConfigDir:         DefaultSMBConfigDir,
		ThumbnailEnabled:     true,
		ThumbnailDir:         "",
	}
}

// Holder holds the live values. Every reader goes through it, so a change
// reaches every subsystem that asks rather than the ones somebody remembered.
type Holder struct {
	mu  sync.RWMutex
	val Values

	// apply propagates values to the running components. The wiring installs it,
	// being the only layer that knows which components those are.
	apply func(Values)
}

// New creates a holder around the values present at server start.
func New(v Values) *Holder { return &Holder{val: v} }

// Get is the live values.
func (h *Holder) Get() Values {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.val
}

// OnApply registers the function that propagates changes to live components.
func (h *Holder) OnApply(fn func(Values)) {
	h.mu.Lock()
	h.apply = fn
	h.mu.Unlock()
}

// Set swaps in new values and propagates them to the live components.
//
// The callback runs after the lock is released. A callback that read the
// holder while it was still held would deadlock, and a callback reading the
// values it was just handed is the obvious thing to write.
func (h *Holder) Set(v Values) {
	h.mu.Lock()
	h.val = v
	fn := h.apply
	h.mu.Unlock()
	if fn != nil {
		fn(v)
	}
}
