//go:build linux

// Package settings owns the settings document and applies changes to running services.
// It deliberately depends on typed callbacks for product services rather than
// carrying an application engine through the settings path.
package settings

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/admin/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/oidc"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/concurrency"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/system/jail"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/sizeguard"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// Hosts is the live host boundary used by the request chain.
type Hosts struct {
	App     []string
	Content []string
}

// Options supplies the narrow runtime effects settings can update. A nil
// callback means that optional subsystem is not present in this deployment.
type Options struct {
	State   *state.DB
	Auth    *auth.Service
	Logger  *slog.Logger
	DataDir string
	Files   []sizeguard.File

	SearchBounds     func(concurrency int, deadline time.Duration)
	ArchiveLimit     func(limit int)
	WatchBounds      func(hotSet, fullThreshold int)
	OpenIndex        func(context.Context)
	SetSMBTOTPPolicy func(auth.TOTPPolicy)
	ApplyHomes       func(context.Context, runtimecfg.Values)
	ApplyThumbnails  func(context.Context, runtimecfg.Values)
	SetRateLimits    func(perSecond float64, burst float64)
	BuildOIDC        func(context.Context, *runtimecfg.OIDC) *oidc.Client
	SetOIDC          func(*oidc.Client, string)
	SetHosts         func(Hosts)
}

// Coordinator reads, stores and applies the settings document. It is safe for
// request readers while a save replaces the live values.
type Coordinator struct {
	opts            Options
	mu              sync.RWMutex
	hosts           Hosts
	trusted         []netip.Prefix
	allowedOrigins  []string
	compatCanonical string
	values          runtimecfg.Values

	bindMu     sync.Mutex
	boundAddr  string
	bindPinned bool
	onBind     func(string)

	hostChangeMu sync.Mutex
	onHostChange func()

	guardMu   sync.Mutex
	guardStop context.CancelFunc
}

// New creates a coordinator over the settings store and runtime callbacks.
func New(opts Options) *Coordinator {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Coordinator{opts: opts, values: runtimecfg.Defaults()}
}

// Load reads the stored document and applies it to the running services.
func (c *Coordinator) Load(ctx context.Context) {
	values := c.Values(ctx)
	var provider *oidc.Client
	if values.OIDC != nil && c.opts.BuildOIDC != nil {
		provider = c.opts.BuildOIDC(ctx, values.OIDC)
	}

	c.mu.Lock()
	appHostsChanged := !slices.Equal(c.hosts.App, values.AppHosts)
	c.hosts = Hosts{App: slices.Clone(values.AppHosts), Content: slices.Clone(values.ContentHosts)}
	c.trusted = parsePrefixes(values.TrustedProxy, c.opts.Logger)
	c.allowedOrigins = slices.Clone(values.AllowedOrigins)
	c.compatCanonical = values.CompatCanonicalURL
	c.values = values
	c.mu.Unlock()
	if c.opts.SetHosts != nil {
		c.opts.SetHosts(Hosts{App: slices.Clone(values.AppHosts), Content: slices.Clone(values.ContentHosts)})
	}
	if appHostsChanged {
		c.hostChangeMu.Lock()
		onChange := c.onHostChange
		c.hostChangeMu.Unlock()
		if onChange != nil {
			onChange()
		}
	}

	if c.opts.SetOIDC != nil {
		c.opts.SetOIDC(provider, values.OIDCDisplayName)
	}
	if c.opts.SearchBounds != nil {
		c.opts.SearchBounds(values.SearchConcurrentSSD, values.SearchDeadlineSSD)
	}
	if c.opts.ArchiveLimit != nil {
		c.opts.ArchiveLimit(values.ArchiveMaxConcurrent)
	}
	if c.opts.WatchBounds != nil {
		c.opts.WatchBounds(values.WatchHotSetMax, values.WatchFullThreshold)
	}
	if c.opts.OpenIndex != nil {
		c.opts.OpenIndex(ctx)
	}
	if c.opts.SetSMBTOTPPolicy != nil {
		c.opts.SetSMBTOTPPolicy(smbTOTPPolicyOf(values.SMBTOTPPolicy, c.opts.Logger))
	}
	if c.opts.SetRateLimits != nil {
		c.opts.SetRateLimits(values.RatePerSec, float64(values.RateBurst))
	}
	if c.opts.ApplyHomes != nil {
		c.opts.ApplyHomes(ctx, values)
	}
	c.applySizeGuard(ctx, values)
	if c.opts.ApplyThumbnails != nil {
		c.opts.ApplyThumbnails(ctx, values)
	}

	c.bindMu.Lock()
	onBind, was, pinned := c.onBind, c.boundAddr, c.bindPinned
	if !pinned {
		c.boundAddr = values.Listen
	}
	c.bindMu.Unlock()
	if onBind != nil && !pinned && values.Listen != "" && values.Listen != was {
		onBind(values.Listen)
	}
}

// Values resolves the stored document over defaults without changing live
// services. This is used by the settings GET and restart policy checks.
func (c *Coordinator) Values(ctx context.Context) runtimecfg.Values {
	if c.opts.State == nil {
		return runtimecfg.Defaults()
	}
	return runtimecfg.Load(ctx, c.opts.State, runtimecfg.Defaults(), c.opts.Logger)
}

// Live returns a snapshot of the values most recently applied.
func (c *Coordinator) Live() runtimecfg.Values {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.values
}

// Hosts returns a copy of the live host boundary.
func (c *Coordinator) Hosts() Hosts {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Hosts{App: slices.Clone(c.hosts.App), Content: slices.Clone(c.hosts.Content)}
}

// TrustedProxies returns a copy of the live trusted proxy ranges.
func (c *Coordinator) TrustedProxies() []netip.Prefix {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.trusted)
}

// AllowedOrigins returns a copy of the live CORS origin list.
func (c *Coordinator) AllowedOrigins() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.allowedOrigins)
}

// CompatCanonicalURL returns the live compatibility fallback URL.
func (c *Coordinator) CompatCanonicalURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.compatCanonical
}

// ProbeHost returns the first application host, or empty before setup.
func (c *Coordinator) ProbeHost() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.hosts.App) == 0 {
		return ""
	}
	return c.hosts.App[0]
}

// OnAppHostChange registers the process callback for a live host change.
func (c *Coordinator) OnAppHostChange(fn func()) {
	c.hostChangeMu.Lock()
	c.onHostChange = fn
	c.hostChangeMu.Unlock()
}

// OnBindChange registers the process callback for a live listen address change.
func (c *Coordinator) OnBindChange(current string, pinned bool, fn func(string)) {
	c.bindMu.Lock()
	c.boundAddr, c.bindPinned, c.onBind = current, pinned, fn
	c.bindMu.Unlock()
}

// BindPinned reports whether the process command line owns the listen address.
func (c *Coordinator) BindPinned() bool {
	c.bindMu.Lock()
	defer c.bindMu.Unlock()
	return c.bindPinned
}

// WouldLoosenHardening reports whether the stored restart policy is weaker
// than the policy installed in this process.
func (c *Coordinator) WouldLoosenHardening(ctx context.Context, installed jail.Policy) bool {
	return c.Values(ctx).Hardening > installed
}

// StopSizeGuard stops the sampler and releases any write block it installed.
func (c *Coordinator) StopSizeGuard() {
	c.guardMu.Lock()
	defer c.guardMu.Unlock()
	if c.guardStop != nil {
		c.guardStop()
		c.guardStop = nil
	}
	guard := sizeguard.New(c.opts.DataDir, c.opts.Files)
	guard.Unblock()
}

func (c *Coordinator) applySizeGuard(ctx context.Context, values runtimecfg.Values) {
	c.guardMu.Lock()
	defer c.guardMu.Unlock()
	if c.guardStop != nil {
		c.guardStop()
		c.guardStop = nil
	}
	guard := sizeguard.New(c.opts.DataDir, c.opts.Files)
	cfg := sizeguard.Config{MinFreeBytes: values.DBGuard.MinFreeBytes, MaxBytes: values.DBGuard.MaxBytes, Interval: values.DBGuard.Interval}
	if !cfg.Enabled() {
		guard.Unblock()
		return
	}
	loop, stop := context.WithCancel(context.WithoutCancel(ctx))
	c.guardStop = stop
	concurrency.Go(loop, "the database size guard", func() {
		guard.Run(loop, cfg, func(st sizeguard.State) {
			if st.Blocked {
				c.opts.Logger.Error("the databases are refusing writes", "reason", st.Reason, "store_bytes", st.StoreBytes, "available_bytes", st.AvailableBytes)
				return
			}
			c.opts.Logger.Info("the databases are accepting writes again", "store_bytes", st.StoreBytes, "available_bytes", st.AvailableBytes)
		})
	})
}

// StoreConfigSecret seals and stores a credential. Empty clears the row.
func (c *Coordinator) StoreConfigSecret(ctx context.Context, name, plain string) error {
	if plain == "" {
		return c.opts.State.DeleteConfigSecret(ctx, name)
	}
	sealed, ver, err := c.opts.Auth.SealConfigSecret(name, []byte(plain))
	if err != nil {
		return err
	}
	return c.opts.State.WriteConfigSecret(ctx, name, state.ConfigSecret{Value: sealed, KeyVer: ver})
}

// ConfigSecret opens a stored credential, or reports that none exists.
func (c *Coordinator) ConfigSecret(ctx context.Context, name string) (string, bool, error) {
	row, ok, err := c.opts.State.ReadConfigSecret(ctx, name)
	if err != nil || !ok {
		return "", false, err
	}
	plain, err := c.opts.Auth.OpenConfigSecret(name, row.Value, row.KeyVer)
	if err != nil {
		return "", false, err
	}
	return string(plain), true, nil
}

// HasConfigSecret reports whether a credential row exists without opening it.
func (c *Coordinator) HasConfigSecret(ctx context.Context, name string) bool {
	_, ok, err := c.opts.State.ReadConfigSecret(ctx, name)
	if err != nil {
		c.opts.Logger.Warn("could not read a configuration secret", "name", name, "error", err)
		return false
	}
	return ok
}

// ParsePrefixesForTest exposes the safe proxy-range parser to package tests.
func ParsePrefixesForTest(raw []string) []netip.Prefix {
	return parsePrefixes(raw, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func parsePrefixes(raw []string, logger *slog.Logger) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, aerr := netip.ParseAddr(entry)
			if aerr != nil {
				logger.Warn("a trusted proxy range was not understood and is ignored", "entry", entry, "error", err)
				continue
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, p)
	}
	return out
}

func smbTOTPPolicyOf(name string, logger *slog.Logger) auth.TOTPPolicy {
	switch name {
	case runtimecfg.DefaultSMBTOTPPolicy:
		return auth.TOTPRequireSeparate
	case "block":
		return auth.TOTPBlock
	default:
		logger.Warn("the stored SMB second-factor policy was not understood; enrolled accounts are blocked from the protocol", "policy", name)
		return auth.TOTPBlock
	}
}
