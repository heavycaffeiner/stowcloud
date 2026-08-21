package server

import (
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
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

	// SMB is what the sidecar publishes, and where to reach it. Off by
	// default: SMB starts explicitly.
	SMB SMBConfig

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
	SMB struct {
		Enabled         bool     `toml:"enabled"`
		Workgroup       string   `toml:"workgroup"`
		ServerName      string   `toml:"server_name"`
		Interfaces      []string `toml:"interfaces"`
		ServiceUser     string   `toml:"service_user"`
		AllowPublicBind bool     `toml:"allow_public_bind"`
		ConfigDir       string   `toml:"config_dir"`
		AgentSocket     string   `toml:"agent_socket"`
	} `toml:"smb"`
	Shares []struct {
		Name             string `toml:"name"`
		HostPath         string `toml:"host_path"`
		SharedExternally bool   `toml:"shared_externally"`
	} `toml:"shares"`
}

// defaults are the compiled-in values a missing key inherits. A D5 constant
// is the outer bound; the default sits within it.
const (
	defaultSMBConfigDir = "/config/smb"
	defaultWorkgroup    = "WORKGROUP"
	defaultListen       = ":8443"
	defaultRatePerSec   = 20
	defaultRateBurst    = 100
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
	cfg.SMB.ConfigDir = r.SMB.ConfigDir
	if cfg.SMB.ConfigDir == "" {
		cfg.SMB.ConfigDir = defaultSMBConfigDir
	}
	cfg.SMB.AgentSocket = r.SMB.AgentSocket
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
		cfg.Shares = append(cfg.Shares, ShareConfig{
			Name: sh.Name, Host: sh.HostPath, SharedExternally: sh.SharedExternally,
		})
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
