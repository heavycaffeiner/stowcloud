// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Config is the typed configuration every other package accepts. The TOML
// file is parsed into a raw struct first; this is the validated form, and an
// out-of-range value is a startup refusal naming the key. Config parsing is a
// trust boundary, so the validation lives here and nowhere else.
type Config struct {
	// DataDir is where the store, the TLS material and the setup token live.
	DataDir string
	// Listen is the bind address, always TLS.
	Listen string

	AppHosts     []string
	ContentHosts []string
	TrustedProxy []netip.Prefix

	RatePerSec float64
	RateBurst  int

	// AppHost is the host the login screen and the API are reached under, the
	// first entry of AppHosts, used by the healthcheck's TLS verification.
	AppHost string

	// Hardening is what the operator asked of the sandbox. The shipped default
	// refuses to start when a layer cannot be applied.
	Hardening jail.Policy

	// DBGuard bounds the store's disk use. Off unless asked for: an instance
	// that stops accepting writes because a cache grew is worse than one that
	// uses more disk than expected, which is the opposite of the stance
	// hardening takes and deliberately so.
	DBGuard store.GuardConfig

	// SMB is what the sidecar publishes, and where to reach it. Off by
	// default: SMB starts explicitly.
	SMB SMBConfig

	// OIDC is the single-sign-on client, or nil when none is configured. Link
	// only: the provider authenticates and never creates an account here.
	OIDC *oidc.Config
	// OIDCDisplayName is what the sign-in button says.
	OIDCDisplayName string

	// Shares are the folders this server serves, named by the operator. A
	// deployment with none starts and has nothing to show, which is what a
	// first run looks like before anyone has said what to serve.
	Shares []ShareConfig
}

// ShareConfig is one configured folder.
type ShareConfig struct {
	Name string
	// Host is the path as this process sees it, which inside a container is
	// the path the folder is mounted at rather than the path on the machine.
	Host string
	// SharedExternally marks a folder another program also writes. Nothing on
	// a filesystem says so, which is why it is the operator who says it.
	SharedExternally bool
	// TrashEnabled keeps deleted items in the share rather than removing them.
	// Off by default, because trash is disk somebody has to reclaim and a
	// server that silently keeps everything is a server that fills up.
	TrashEnabled bool
	// Symlink is what this share does with a symlink. Per share rather than
	// global: one folder of curated links and one folder of uploads want
	// different answers, and the resolver takes it per root already.
	//
	// Deny by default, which is the restrictive one.
	Symlink vfs.SymlinkPolicy
}

// SMBConfig is the publishing half of the SMB settings, alongside the render
// settings the smb package owns.
type SMBConfig struct {
	Render smb.Config
	// ConfigDir is the directory both containers mount, where the rendered
	// files go.
	ConfigDir string
	// AgentSocket is where the sidecar listens. Empty means no sidecar, which
	// is a bare-metal deployment where something else applies the files.
	AgentSocket string
	// ServiceGID is the group every rendered account belongs to. It has to be
	// the group the sidecar's own image creates, which the agent checks and
	// refuses over.
	//
	// Configurable rather than compiled in, because a bare-metal install picks
	// its own service account and the image's number is not a fact about that
	// machine. The default is the image's.
	ServiceGID uint32
}

// raw is the TOML shape, parsed then validated. The field names are the
// config file's contract and stay stable however the typed form changes.
type raw struct {
	Server struct {
		DataDir string `toml:"data_dir"`
		Listen  string `toml:"listen"`
	} `toml:"server"`
	HTTP struct {
		AppHosts          []string `toml:"app_hosts"`
		ContentHosts      []string `toml:"content_hosts"`
		TrustedProxyCIDRs []string `toml:"trusted_proxy_cidrs"`
	} `toml:"http"`
	Rate struct {
		PerSec float64 `toml:"per_sec"`
		Burst  int     `toml:"burst"`
	} `toml:"rate"`
	Security struct {
		Hardening string `toml:"hardening"`
	} `toml:"security"`
	DB struct {
		SizeGuard    bool    `toml:"size_guard"`
		MaxBytes     *uint64 `toml:"max_bytes"`
		MinFreeBytes *uint64 `toml:"min_free_bytes"`
	} `toml:"db"`
	OIDC struct {
		Enabled               bool     `toml:"enabled"`
		Issuer                string   `toml:"issuer"`
		ClientID              string   `toml:"client_id"`
		ClientSecret          string   `toml:"client_secret"`
		DisplayName           string   `toml:"display_name"`
		Scopes                []string `toml:"scopes"`
		AllowPrivateEndpoints bool     `toml:"allow_private_endpoints"`
		CACertFile            string   `toml:"ca_cert_file"`
	} `toml:"oidc"`
	SMB struct {
		Enabled         bool     `toml:"enabled"`
		Workgroup       string   `toml:"workgroup"`
		ServerName      string   `toml:"server_name"`
		Interfaces      []string `toml:"interfaces"`
		ServiceUser     string   `toml:"service_user"`
		ServiceGID      *uint32  `toml:"service_gid"`
		AllowPublicBind bool     `toml:"allow_public_bind"`
		ConfigDir       string   `toml:"config_dir"`
		AgentSocket     string   `toml:"agent_socket"`
	} `toml:"smb"`
	Shares []struct {
		Name             string `toml:"name"`
		HostPath         string `toml:"host_path"`
		SharedExternally bool   `toml:"shared_externally"`
		TrashEnabled     bool   `toml:"trash_enabled"`
		SymlinkPolicy    string `toml:"symlink_policy"`
	} `toml:"shares"`
}

// defaults are the compiled-in values a missing key inherits. A D5 constant
// is the outer bound; the default sits within it.
const (
	defaultSMBConfigDir = "/config/smb"
	// defaultServiceGID is the group Dockerfile.smb creates. The two have to
	// agree, and the agent says so loudly when they do not: it refuses the sync
	// with "no group exists for N" rather than syncing accounts nothing can
	// resolve.
	defaultServiceGID = 1000
	defaultWorkgroup  = "WORKGROUP"
	// defaultServiceUser is the account Dockerfile.smb creates, the same pairing
	// as defaultServiceGID above.
	defaultServiceUser = "scsvc"
	defaultListen      = ":8443"
	defaultRatePerSec  = 20
	defaultRateBurst   = 100
)

// Load reads and validates the config file. An absent file is a startup
// refusal: the product's surface is defined by configuration, and a server
// that guesses its own origin is not a guard.
func Load(path string) (*Config, error) {
	rawBytes, err := os.ReadFile(path) //nolint:gosec // G304 reads the variable: the path is the operator's own argument, never request input.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var r raw
	if err := toml.Unmarshal(rawBytes, &r); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return Validate(r)
}

// Validate turns the parsed raw values into the typed Config. Every refusal
// names the key, which is what an operator who made the typo can act on.
func Validate(r raw) (*Config, error) {
	cfg := &Config{}
	cfg.DataDir = r.Server.DataDir
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("server.data_dir: must not be empty")
	}
	cfg.Listen = r.Server.Listen
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	cfg.AppHosts = r.HTTP.AppHosts
	cfg.ContentHosts = r.HTTP.ContentHosts
	if len(cfg.AppHosts) == 0 {
		return nil, fmt.Errorf("http.app_hosts: at least one host is required, and a guard that learned its origin from the request it guards is not a guard")
	}
	cfg.AppHost = cfg.AppHosts[0]
	for _, cidr := range r.HTTP.TrustedProxyCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			return nil, fmt.Errorf("http.trusted_proxy_cidrs: %q is not a CIDR", cidr)
		}
		cfg.TrustedProxy = append(cfg.TrustedProxy, p)
	}
	cfg.RatePerSec = r.Rate.PerSec
	if cfg.RatePerSec <= 0 {
		cfg.RatePerSec = defaultRatePerSec
	}
	cfg.RateBurst = r.Rate.Burst
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = defaultRateBurst
	}
	if cfg.RatePerSec > 10_000 || cfg.RateBurst > 10_000 {
		return nil, fmt.Errorf("rate: a value over 10000 is beyond the compiled-in outer bound")
	}

	// Single sign-on. Nil rather than a disabled client, because a client that
	// exists and refuses is one every caller has to remember to ask about.
	if r.OIDC.Enabled {
		if r.OIDC.Issuer == "" || r.OIDC.ClientID == "" {
			return nil, fmt.Errorf("oidc: issuer and client_id are required when single sign-on is enabled")
		}
		cfg.OIDC = &oidc.Config{
			Issuer:                r.OIDC.Issuer,
			ClientID:              r.OIDC.ClientID,
			ClientSecret:          secret.New([]byte(r.OIDC.ClientSecret)),
			Scopes:                r.OIDC.Scopes,
			AllowPrivateEndpoints: r.OIDC.AllowPrivateEndpoints,
			CACertFile:            r.OIDC.CACertFile,
		}
		cfg.OIDCDisplayName = r.OIDC.DisplayName
		if cfg.OIDCDisplayName == "" {
			cfg.OIDCDisplayName = "Single sign-on"
		}
	}

	// SMB. Every value that reaches the rendered file is checked by the
	// renderer, which refuses rather than escapes: that format has no escape
	// surviving its own continuation and substitution rules. What is checked
	// here is only what the renderer never sees.
	cfg.SMB.Render = smb.Config{
		Enabled:         r.SMB.Enabled,
		Workgroup:       r.SMB.Workgroup,
		ServerName:      r.SMB.ServerName,
		Interfaces:      r.SMB.Interfaces,
		ServiceUser:     r.SMB.ServiceUser,
		AllowPublicBind: r.SMB.AllowPublicBind,
	}
	if cfg.SMB.Render.Workgroup == "" {
		cfg.SMB.Render.Workgroup = defaultWorkgroup
	}
	// The service account every SMB connection runs as. Defaulted rather than
	// left blank so the settings screen shows the name a deployment would get,
	// and so turning SMB on from that screen does not also require inventing
	// one. It is still required when the config file enables SMB itself: a file
	// that turns it on without naming the account is a file to correct, not one
	// to quietly complete.
	if cfg.SMB.Render.ServiceUser == "" && !r.SMB.Enabled {
		cfg.SMB.Render.ServiceUser = defaultServiceUser
	}
	cfg.SMB.ConfigDir = r.SMB.ConfigDir
	if cfg.SMB.ConfigDir == "" {
		cfg.SMB.ConfigDir = defaultSMBConfigDir
	}
	cfg.SMB.AgentSocket = r.SMB.AgentSocket
	cfg.SMB.ServiceGID = defaultServiceGID
	if r.SMB.ServiceGID != nil {
		// Zero is root's group. An account file that puts every SMB account in
		// it is not a configuration anybody means to write, and the agent runs
		// as root, so it would be applied rather than refused.
		if *r.SMB.ServiceGID == 0 {
			return nil, fmt.Errorf("smb.service_gid: 0 is root's group, which no service account may belong to")
		}
		cfg.SMB.ServiceGID = *r.SMB.ServiceGID
	}
	if r.SMB.Enabled {
		if cfg.SMB.Render.ServiceUser == "" {
			return nil, fmt.Errorf("smb.service_user: required when SMB is enabled, because every connection runs as one account")
		}
		// Rendered now rather than at the first publish, so a configuration
		// the renderer refuses is a startup refusal naming the key instead of
		// a settings screen failing later.
		if _, err := smb.Render(cfg.SMB.Render, nil); err != nil {
			return nil, fmt.Errorf("smb: %w", err)
		}
	}

	// An absent value takes the shipped default rather than the zero one by
	// accident. They happen to be the same policy, and relying on that would
	// make renumbering the constants a silent downgrade of every deployment
	// that never set the key.
	seen := map[string]bool{}
	for i, sh := range r.Shares {
		if sh.Name == "" {
			return nil, fmt.Errorf("shares[%d]: a share needs a name", i)
		}
		if sh.HostPath == "" {
			return nil, fmt.Errorf("shares[%d] (%q): a share needs a host_path", i, sh.Name)
		}
		if !strings.HasPrefix(sh.HostPath, "/") {
			return nil, fmt.Errorf("shares[%d] (%q): host_path must be absolute, and inside a container it is the path the folder is mounted at", i, sh.Name)
		}
		// Two shares under one name is one name resolving to two folders, and
		// which one a path meant would depend on registration order.
		if seen[sh.Name] {
			return nil, fmt.Errorf("shares[%d]: %q is named twice", i, sh.Name)
		}
		seen[sh.Name] = true
		// The symlink policy, refused by name rather than defaulted. An
		// operator who wrote a value this build does not implement believes
		// the share follows links it does not.
		policy := vfs.SymlinkDeny
		if sh.SymlinkPolicy != "" {
			p, perr := vfs.ParseSymlinkPolicy(sh.SymlinkPolicy)
			if perr != nil {
				return nil, fmt.Errorf("shares[%d] (%q): %w", i, sh.Name, perr)
			}
			policy = p
		}
		cfg.Shares = append(cfg.Shares, ShareConfig{
			Name: sh.Name, Host: sh.HostPath, SharedExternally: sh.SharedExternally,
			TrashEnabled: sh.TrashEnabled, Symlink: policy,
		})
	}

	// The size guard. size_guard is the switch; a bound with the switch off is
	// stored and not applied, which is what lets an operator set the numbers
	// before turning it on.
	if r.DB.SizeGuard {
		if r.DB.MinFreeBytes != nil {
			cfg.DBGuard.MinFreeBytes = *r.DB.MinFreeBytes
		}
		if r.DB.MaxBytes != nil {
			cfg.DBGuard.MaxBytes = *r.DB.MaxBytes
		}
		if !cfg.DBGuard.Enabled() {
			return nil, fmt.Errorf(
				"db.size_guard is on and neither db.min_free_bytes nor db.max_bytes is set, " +
					"so nothing would ever trip it")
		}
	}

	cfg.Hardening = jail.Required
	if r.Security.Hardening != "" {
		pol, perr := jail.ParsePolicy(r.Security.Hardening)
		if perr != nil {
			return nil, fmt.Errorf("security.hardening: %w", perr)
		}
		cfg.Hardening = pol
	}
	return cfg, nil
}
