//go:build linux

// Reading the operator's settings into the running server.
//
// The chain reads the host lists, the trusted proxy ranges and the rate
// limits per request, so they have to be loaded from the stored document at
// boot rather than left at their zero values. They were: nothing assigned
// them, so a deployment with configured hosts ran permanently in first-boot
// mode, admitting only private clients and ignoring what the operator had
// saved.
package lifecycle

import (
	"context"
	"io"
	"log/slog"
	"net/netip"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/oidc"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
)

// loadSettings reads the stored document over the compiled-in defaults and
// applies what the chain consults live.
//
// A document that cannot be read is a warning rather than a failure: the
// defaults are a working deployment, and refusing to boot over an unreadable
// settings row would take a server down for a value it could run without.
func (e *Engine) loadSettings(ctx context.Context) {
	values := runtimecfg.Load(ctx, e.State, runtimecfg.Defaults(), e.logger)

	// Built outside the lock: constructing the client reads the sealed secret
	// and can reach the database, and holding the settings lock across that
	// would block every request that reads a host list.
	var provider *oidc.Client
	if values.OIDC != nil {
		provider = e.buildOIDCClient(ctx, &oidcSettings{
			Issuer:                values.OIDC.Issuer,
			ClientID:              values.OIDC.ClientID,
			Scopes:                values.OIDC.Scopes,
			AllowPrivateEndpoints: values.OIDC.AllowPrivateEndpoints,
			CACertFile:            values.OIDC.CACertFile,
		})
	}

	e.settingsMu.Lock()
	e.appHosts = middleware.Hosts{
		App:     values.AppHosts,
		Content: values.ContentHosts,
	}
	e.trusted = parsePrefixes(values.TrustedProxy, e)
	e.oidcClient = provider
	e.oidcName = values.OIDCDisplayName
	e.settingsMu.Unlock()

	// The search and archive bounds an administrator adjusts. Applied through
	// each service's own setter, which leaves the search concurrency gate
	// alone: it is a buffered channel established at construction, and
	// swapping it while queries are in flight would lose the slots they hold.
	if e.Search != nil {
		concurrency, deadline := values.SearchConcurrentSSD, values.SearchDeadlineSSD
		e.Search.SetBounds(concurrency, deadline)
	}
	preview.SetMaxListed(values.ArchiveMaxConcurrent)

	// The watcher's own setter rather than a rebuild: a rebuild would drop
	// every live inotify watch and every pinned subscription, the latter
	// coming from live client WebSocket state that is never replayed, and
	// force every directory back through a cold addWatch. A lowered
	// hot_set_max evicts down to the new cap immediately inside the setter.
	if e.watcher != nil {
		e.watcher.SetBounds(values.WatchHotSetMax, values.WatchFullThreshold)
	}

	// The name index. Re-invoked here rather than only at boot, so a save
	// attaches or detaches it, and its updater, immediately: this is what
	// makes turning search.name_index_enabled off actually drop the live
	// index instead of leaving it running under a setting that says off.
	e.openSearchIndex(ctx)

	// Whether an account with a second factor may reach the file-sharing
	// protocol. The value was loaded and then dropped, so an operator who set
	// the blocking policy kept running under the permissive one: every
	// enrolled account held the access the operator had revoked, and the
	// settings screen showed the revocation.
	if e.Auth != nil {
		e.Auth.SetSMBTOTPPolicy(smbTOTPPolicyOf(values.SMBTOTPPolicy, e))
	}

	// The limiter's own setter rather than a fresh limiter: it holds a bucket
	// per client, and replacing it would discard every one of them, handing a
	// full burst to whoever was being throttled at that moment.
	e.limiter.SetLimits(values.RatePerSec, float64(values.RateBurst))

	// Home folders. Applied here rather than only at boot, so turning them on
	// or moving their root takes effect without a restart: the core treats a
	// second call as a re-registration of the same reserved share.
	if e.Core != nil {
		e.applyHomes(ctx, values)
	}

	// The database size guard. Started, restarted or stopped here, because
	// the sampler holds the configuration it was given and a change has to
	// replace the goroutine rather than mutate what it is reading.
	e.applySizeGuard(ctx, values)

	// The bind address, when it moved. The listener belongs to the process
	// rather than to this engine, so the change is handed out rather than
	// applied here: a swap that fails leaves the old socket answering, and
	// only the caller that owns the supervisor can decide that.
	e.bindMu.Lock()
	onBind, was := e.onBind, e.boundAddr
	e.boundAddr = values.Listen
	e.bindMu.Unlock()

	if onBind != nil && values.Listen != "" && values.Listen != was {
		onBind(values.Listen)
	}
}

// OnBindChange registers what to do when a settings save moves the listen
// address.
//
// The listener belongs to the process, not to this engine: it was built before
// Open and it outlives any one mount. So the engine reports the change and the
// caller decides, which is what lets a failed rebind leave the old socket
// answering.
//
// The address this engine already believes it is on is recorded here, so
// registering the hook does not immediately fire it for the address the caller
// just bound.
func (e *Engine) OnBindChange(current string, fn func(addr string)) {
	e.bindMu.Lock()
	defer e.bindMu.Unlock()
	e.boundAddr = current
	e.onBind = fn
}

// parsePrefixes reads the operator's proxy list into the form the client
// resolver compares against.
//
// An entry that does not parse is dropped with a warning rather than failing
// the boot. Dropping is the safe direction: a range that is not trusted means
// a forwarded header from it is ignored, which refuses more than it admits.
func parsePrefixes(raw []string, e *Engine) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			// A bare address is the ordinary way an operator writes a single
			// proxy, so it is accepted as its own single-address range rather
			// than refused for missing a mask.
			addr, aerr := netip.ParseAddr(entry)
			if aerr != nil {
				e.logger.Warn("a trusted proxy range was not understood and is ignored",
					"entry", entry, "error", err)
				continue
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, p)
	}
	return out
}

// ParsePrefixesForTest exposes the proxy-range parse.
//
// Exported for a test because the failure worth pinning is a wildcard: an
// entry that does not parse must be dropped rather than become 0.0.0.0/0,
// which would trust every proxy on the internet. Driving that through the
// server would need a request from an address the test cannot bind.
func ParsePrefixesForTest(raw []string) []netip.Prefix {
	return parsePrefixes(raw, &Engine{logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// smbTOTPPolicyOf reads the stored policy name.
//
// An unrecognised name takes the blocking policy, not the permissive one.
// The two spellings a document can carry are the ones below, so anything else
// is a value nobody meant, and guessing wrong in the permissive direction
// hands protocol access to accounts an operator may have been trying to shut
// out.
//
// This makes the blocking branch untestable in isolation: misspelling "block"
// sends the value to the default, which blocks as well, so no test can tell
// the two apart. What the tests pin instead is the permissive branch, which
// is the one whose loss would be a security change.
func smbTOTPPolicyOf(name string, e *Engine) auth.TOTPPolicy {
	switch name {
	case runtimecfg.DefaultSMBTOTPPolicy:
		return auth.TOTPRequireSeparate
	case "block":
		return auth.TOTPBlock
	default:
		e.logger.Warn("the stored SMB second-factor policy was not understood; "+
			"enrolled accounts are blocked from the protocol", "policy", name)
		return auth.TOTPBlock
	}
}
