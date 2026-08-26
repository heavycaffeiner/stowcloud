// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"net/netip"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// Config is the typed configuration every other package accepts. It is built
// from the stored settings, which is the only place a deployment is
// configured: there is no config file.
//
// It is a snapshot rather than a live view. The values it holds are the ones
// read when the listener and the guards were built; the fields that move
// without a restart are held by runtimecfg.Holder and read from there.
type Config struct {
	// DataDir is where the store, the TLS material and the setup token live.
	// It is the one thing that cannot come from the database, because it is
	// where the database is.
	DataDir string
	// Listen is the bind address, always TLS.
	Listen string

	AppHosts     []string
	TrustedProxy []netip.Prefix

	RatePerSec float64
	RateBurst  int

	// AppHost is the host the login screen and the API are reached under, the
	// first entry of AppHosts, used by the healthcheck's TLS verification.
	// Empty before setup has named one, which is what a first boot looks like.
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
	ServiceGID uint32
}

// FromValues builds the typed configuration from the stored settings.
//
// Nothing here refuses. Every value has already been checked twice: at save
// time, where an administrator was watching and a bad one was named and
// refused, and at load time, where a stored value outside its bound was
// clamped or dropped with a line in the log. A third refusal at this point
// would only be able to stop the server starting, which is the failure the
// whole phase exists to avoid.
//
// clientSecret is the OIDC secret, opened from the store by the caller that
// holds the master key. It is passed in rather than read here because this
// package has no key.
func FromValues(dataDir string, v runtimecfg.Values, clientSecret string) *Config {
	cfg := &Config{
		DataDir:      dataDir,
		Listen:       v.Listen,
		AppHosts:     v.AppHosts,
		TrustedProxy: runtimecfg.ParsePrefixes(v.TrustedProxy),
		RatePerSec:   v.RatePerSec,
		RateBurst:    v.RateBurst,
		Hardening:    v.Hardening,
		DBGuard:      v.DBGuard,
	}
	if len(cfg.AppHosts) > 0 {
		cfg.AppHost = cfg.AppHosts[0]
	}
	cfg.SMB = SMBConfig{
		Render: smb.Config{
			Enabled:         v.SMB.Enabled,
			Workgroup:       v.SMB.Workgroup,
			ServerName:      v.SMB.ServerName,
			Interfaces:      v.SMB.Interfaces,
			ServiceUser:     v.SMB.ServiceUser,
			AllowPublicBind: v.SMB.AllowPublicBind,
		},
		ConfigDir:   v.SMB.ConfigDir,
		AgentSocket: v.SMB.AgentSocket,
		ServiceGID:  v.SMB.ServiceGID,
	}
	if v.OIDC != nil {
		cfg.OIDC = &oidc.Config{
			Issuer:                v.OIDC.Issuer,
			ClientID:              v.OIDC.ClientID,
			ClientSecret:          secret.New([]byte(clientSecret)),
			Scopes:                v.OIDC.Scopes,
			AllowPrivateEndpoints: v.OIDC.AllowPrivateEndpoints,
			CACertFile:            v.OIDC.CACertFile,
		}
		cfg.OIDCDisplayName = v.OIDCDisplayName
	}
	return cfg
}
