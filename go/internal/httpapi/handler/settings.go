// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"
	"net/netip"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
)

// The admin settings surface. A D5 constant is the compiled-in default and
// the outer bound; an administrator moves a value within it, and no request
// path moves any of them. A patch outside the bound is refused naming the
// field, which is what the declared range is for.
//
// The sections whose backing subsystem has not landed in this build yet
// answer 501 with the recognized-not-implemented code: the route exists so
// the client's vocabulary is stable, and the work arrives with the phase that
// owns the subsystem.

// settingsField is one row of the settings screen.
//
// The screen reads one flat list of dotted keys rather than a document shaped
// like the config file. That is what lets it show a field it has no dedicated
// control for instead of dropping it, and it is why the keys here are the
// client's vocabulary rather than this package's: a field under a name the
// screen does not know is a setting nobody can see.
type settingsField struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	// Range is what the field accepts, sent rather than compiled into the
	// screen: every bound is a constant here, and a client carrying its own
	// copy is a client that disagrees with the server about what is legal.
	Range any `json:"range,omitempty"`
	// EmptyMeansKey names what leaving this field empty does, for the two
	// fields where empty is a setting rather than a gap. A catalogue key: the
	// server does not know which language the reader picked.
	//
	// Without it a blank box is indistinguishable from one nobody filled in,
	// and the obvious repair, defaulting the value, would turn on name
	// broadcasting or widen a trust boundary that was deliberately closed.
	EmptyMeansKey *string `json:"empty_means_key,omitempty"`
	// Source says where the live value came from, which is what the revert
	// affordance branches on.
	Source string `json:"source"`
	// RestartRequired is a property of the field, not of this value.
	RestartRequired bool `json:"restart_required"`
	// RunningValue is what the process is actually on, present only when that
	// is not the saved value. Absent in the ordinary case.
	RunningValue any `json:"running_value,omitempty"`
	// ReadonlyReasonKey names why a field cannot be changed here, or is null
	// when it can. A catalogue key rather than a sentence: the server does not
	// know which language the reader picked.
	ReadonlyReasonKey *string `json:"readonly_reason_key"`
}

// listenNow is the address in service, and the stored one when this build has
// no listener to ask (a test harness).
func listenNow(d Deps) string {
	if d.Listen == nil {
		return ""
	}
	return d.Listen()
}

// differs is the running value when it is not the saved one, and nil when the
// two agree: the field is omitted rather than repeating itself.
func differs(saved, running string) any {
	if running == "" || running == saved {
		return nil
	}
	return running
}

// The sources a value can have. There is no config file, so a value either
// came from the compiled-in defaults or from something an administrator saved.
const (
	sourceBuiltin  = "builtin_default"
	sourceOverride = "admin_override"
)

// readonly marks a field this build reports and does not let the screen edit,
// with the reason as a catalogue key.
func readonly(key string) *string { return &key }

// emptyMeans names what an empty value does, for a field where empty is a
// choice rather than a gap.
func emptyMeans(key string) *string { return &key }

func intRange(minimum, maximum int64) map[string]any {
	return map[string]any{"kind": "int", "min": minimum, "max": maximum}
}

func stringListRange(maxItems int) map[string]any {
	return map[string]any{"kind": "string_list", "max_items": maxItems}
}

// Settings answers GET /api/admin/server-settings: the patchable bounds and
// the live values, so a client can show what is stored and what took effect.
func Settings(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, aerr := requireAdmin(r, d.Auth); aerr != nil {
			return aerr
		}

		// The live values, which are the config file's plus whatever an
		// administrator has since saved. Reporting the compiled-in constants
		// instead is what made the screen show the old number after a save
		// that had answered "applied".
		rt := runtimecfg.Defaults()
		if d.Runtime != nil {
			rt = d.Runtime.Get()
		}
		// A field is editable when there is somewhere to put it. Without a
		// settings store this build reports and does not change, and says so
		// rather than offering a control that cannot work.
		var frozen *string
		if d.Runtime == nil {
			frozen = readonly("settings.not_in_this_build")
		}
		sourceOf := func(section, key string) string {
			if d.State == nil {
				return sourceBuiltin
			}
			if storedHas(r, d, section, key) {
				return sourceOverride
			}
			return sourceBuiltin
		}

		fields := []settingsField{
			// The network boundary, which is the one group this build moves
			// live: the host lists and the proxy ranges are what the settings
			// screen's network form patches.
			{
				Key: "app_hosts", Value: d.Hosts.App(),
				Range: stringListRange(32), Source: sourceOf("network", "app_hosts"),
				ReadonlyReasonKey: frozen,
			},
			{
				Key: "trusted_proxies", Value: prefixesToStrings(d.Trusted.Get()),
				Range: stringListRange(32), Source: sourceOf("network", "trusted_proxies"),
				EmptyMeansKey:     emptyMeans("settings.empty_trusts_no_proxy"),
				ReadonlyReasonKey: frozen,
			},
			// The listener's address. Saving it moves the socket: the new one is
			// bound before the old one is touched, so this is live like the
			// lists above rather than something waiting for a restart.
			//
			// The value reported is the address in service, which is not the
			// stored one when a swap could not bind. running_value is what says
			// the two differ.
			{
				Key: "bind", Value: rt.Listen, RunningValue: differs(rt.Listen, listenNow(d)),
				Source:            sourceOf("network", "bind"),
				Range:             map[string]any{"kind": "string"},
				ReadonlyReasonKey: frozen,
			},
			// Where the databases, the certificate and the master key live. It is
			// the one thing that cannot be a setting, because it is where the
			// settings are; it is a process argument and is reported here so the
			// screen can say which directory this server is using.
			{
				Key: "data_dir", Value: d.DataDir, Source: sourceBuiltin,
				ReadonlyReasonKey: readonly("settings.readonly_data_dir"),
			},
			// The sandbox. Applied once, before anything is opened, so it is
			// stored now and applied on the next start.
			{
				Key: "security.hardening", Value: rt.Hardening.String(),
				Range:  map[string]any{"kind": "enum", "values": jail.PolicyNames()},
				Source: sourceOf("security", "hardening"), RestartRequired: true,
				ReadonlyReasonKey: frozen,
			},

			// The bounds an administrator moves. Each carries the live value,
			// not the compiled-in constant: reporting the constant is what made
			// a save answer "applied" and the next read show the old number.
			{
				Key: "rate.per_sec", Value: d.Limiter.Rate(),
				Range:  intRange(runtimecfg.BoundRatePerSec().Min, runtimecfg.BoundRatePerSec().Max),
				Source: sourceOf("rate", "per_sec"), ReadonlyReasonKey: frozen,
			},
			{
				Key: "rate.burst", Value: d.Limiter.Burst(),
				Range:  intRange(runtimecfg.BoundRateBurst().Min, runtimecfg.BoundRateBurst().Max),
				Source: sourceOf("rate", "burst"), ReadonlyReasonKey: frozen,
			},
			{
				Key: "search.max_concurrent_fast", Value: rt.SearchConcurrentSSD,
				Range:  intRange(runtimecfg.BoundSearchConcurrent().Min, runtimecfg.BoundSearchConcurrent().Max),
				Source: sourceOf("search", "max_concurrent_fast"), ReadonlyReasonKey: frozen,
			},
			{
				Key: "search.max_concurrent_slow", Value: rt.SearchConcurrentRot,
				Range:  intRange(runtimecfg.BoundSearchConcurrent().Min, runtimecfg.BoundSearchConcurrent().Max),
				Source: sourceOf("search", "max_concurrent_slow"), ReadonlyReasonKey: frozen,
			},
			{
				Key: "search.walk_deadline_fast_ms", Value: rt.SearchDeadlineSSD.Milliseconds(),
				Range:  intRange(runtimecfg.BoundSearchDeadlineMs().Min, runtimecfg.BoundSearchDeadlineMs().Max),
				Source: sourceOf("search", "walk_deadline_fast_ms"), ReadonlyReasonKey: frozen,
			},
			{
				Key: "search.walk_deadline_slow_ms", Value: rt.SearchDeadlineRot.Milliseconds(),
				Range:  intRange(runtimecfg.BoundSearchDeadlineMs().Min, runtimecfg.BoundSearchDeadlineMs().Max),
				Source: sourceOf("search", "walk_deadline_slow_ms"), ReadonlyReasonKey: frozen,
			},
			{
				Key: "archive.max_concurrent", Value: rt.ArchiveMaxConcurrent,
				Range:  intRange(runtimecfg.BoundArchiveEntries().Min, runtimecfg.BoundArchiveEntries().Max),
				Source: sourceOf("archive", "max_concurrent"), ReadonlyReasonKey: frozen,
			},

			// The watcher takes its bounds when it starts, so a change here is
			// stored and applies on the next start. Saying so is the point: a
			// value that reads as live and is not is the thing an administrator
			// spends an afternoon on.
			{
				Key: "watch.hot_set_max", Value: rt.WatchHotSetMax,
				Range:  intRange(runtimecfg.BoundWatchHotSet().Min, runtimecfg.BoundWatchHotSet().Max),
				Source: sourceOf("watch", "hot_set_max"), RestartRequired: true,
				ReadonlyReasonKey: frozen,
			},
			{
				Key: "watch.full_threshold", Value: rt.WatchFullThreshold,
				Range: intRange(runtimecfg.BoundWatchFullThreshold().Min,
					runtimecfg.BoundWatchFullThreshold().Max),
				Source: sourceOf("watch", "full_threshold"), RestartRequired: true,
				ReadonlyReasonKey: frozen,
			},
			// One transport, so the field is reported and not offered: a select
			// with one option is a control that cannot be used.
			{
				Key: "watch.backend", Value: "inotify", Source: sourceBuiltin,
				ReadonlyReasonKey: readonly("settings.unknown_watch_backend"),
			},

			// Homes. The switch and the root are read when the share is
			// registered, which happens once at startup.
			{
				Key: "homes.enabled", Value: rt.HomesEnabled,
				Range: map[string]any{"kind": "bool"}, Source: sourceOf("homes", "enabled"),
				RestartRequired: true, ReadonlyReasonKey: frozen,
			},
			{
				Key: "homes.root", Value: rt.HomesRoot,
				Range: map[string]any{"kind": "string"}, Source: sourceOf("homes", "root"),
				RestartRequired: true, ReadonlyReasonKey: frozen,
			},
		}
		fields = append(fields, smbFields(d, rt, sourceOf, frozen)...)
		fields = append(fields, oidcFields(d, rt, sourceOf)...)

		return writeJSON(w, http.StatusOK, map[string]any{
			"fields": fields,
			// No sidecar warning to raise when SMB is not published from here,
			// and false rather than absent because the screen draws a banner
			// off it either way.
			"smb_public_bind_warning": false,
		})
	})
}

// toAnyList widens a string list into the shape the probes read, which is the
// decoded-JSON shape the generic section handler passes them.
func toAnyList(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func prefixesToStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

// storedHas reports whether an administrator has saved this field, which is
// what tells an override from the config file's own value on the screen.
//
// A read failure answers false: the value shown is still right, and only the
// label under it is less precise.
func storedHas(r *http.Request, d Deps, section, key string) bool {
	all, err := d.State.Settings(r.Context())
	if err != nil {
		return false
	}
	sec, ok := all[section].(map[string]any)
	if !ok {
		return false
	}
	_, ok = sec[key]
	return ok
}

// smbFields is the sidecar's settings.
//
// Reported whether or not SMB is on, because the screen has to be able to turn
// it on: a section that appears only once the thing is enabled has no control
// for enabling it. Every field needs a restart, and says so: the publisher and
// the credential policy are assembled once at startup.
func smbFields(
	d Deps, rt runtimecfg.Values,
	sourceOf func(string, string) string, frozen *string,
) []settingsField {
	policy := rt.SMB.TOTPPolicy
	if policy == "" {
		policy = "require_separate"
	}
	return []settingsField{
		{
			Key: "smb.enabled", Value: rt.SMB.Enabled,
			Range: map[string]any{"kind": "bool"}, Source: sourceOf("smb", "enabled"),
			RestartRequired: true, ReadonlyReasonKey: frozen,
		},
		{
			Key: "smb.workgroup", Value: rt.SMB.Workgroup,
			Range:           map[string]any{"kind": "string", "max_len": 64},
			Source:          sourceOf("smb", "workgroup"),
			RestartRequired: true, ReadonlyReasonKey: frozen,
		},
		{
			Key: "smb.server_name", Value: rt.SMB.ServerName,
			EmptyMeansKey:   emptyMeans("settings.empty_disables_netbios_name"),
			Range:           map[string]any{"kind": "string", "max_len": 64},
			Source:          sourceOf("smb", "server_name"),
			RestartRequired: true, ReadonlyReasonKey: frozen,
		},
		{
			Key: "smb.service_user", Value: rt.SMB.ServiceUser,
			Range:           map[string]any{"kind": "string", "max_len": 64},
			Source:          sourceOf("smb", "service_user"),
			RestartRequired: true, ReadonlyReasonKey: frozen,
		},
		{
			Key: "smb.allow_public_bind", Value: rt.SMB.AllowPublicBind,
			Range:           map[string]any{"kind": "bool"},
			Source:          sourceOf("smb", "allow_public_bind"),
			RestartRequired: true, ReadonlyReasonKey: frozen,
		},
		{
			Key: "smb.totp_policy", Value: policy,
			Range:           map[string]any{"kind": "string"},
			Source:          sourceOf("smb", "totp_policy"),
			RestartRequired: true, ReadonlyReasonKey: frozen,
		},
		{
			Key: "smb.service_gid", Value: rt.SMB.ServiceGID,
			Range:           intRange(runtimecfg.BoundServiceGID().Min, runtimecfg.BoundServiceGID().Max),
			Source:          sourceOf("smb", "service_gid"),
			RestartRequired: true, ReadonlyReasonKey: frozen,
		},
		// The two the screen must not offer. Both are paths the other side of
		// a container boundary agrees on, so changing one here would move only
		// this side of the pair.
		{
			Key: "smb.config_dir", Value: d.SMBConfigDir, Source: sourceBuiltin,
			ReadonlyReasonKey: readonly("settings.readonly_smb_agent_socket"),
		},
	}
}

// oidcFields is single sign-on as the config file settled it.
//
// Reported and not editable here, all of it. The client is built once at
// startup: it fetches the provider's discovery document, pins what it found
// and holds the certificate pool the back channel uses. Changing an issuer
// under a live client would leave the two halves describing different
// providers.
//
// The client secret is reported by its presence and never by its value. It
// rests sealed under the master key, apart from the settings document; a
// settings surface that could show it would be a settings surface that could
// leak it. Writing it is a write-only field: sending one stores it, and
// sending nothing leaves the stored one alone.
func oidcFields(d Deps, rt runtimecfg.Values, sourceOf func(string, string) string) []settingsField {
	// Every one of these is read when the client is built, which happens once
	// at startup: it fetches the discovery document, pins what it found and
	// holds the certificate pool the back channel uses.
	const restart = true
	cfg := rt.OIDC
	if cfg == nil {
		// Off. The fields are still reported, because a screen that hides the
		// section until it is configured has nowhere to say it is off.
		cfg = &runtimecfg.OIDC{}
	}
	return []settingsField{
		{Key: "oidc.enabled", Value: cfg.Enabled, Range: map[string]any{"kind": "bool"},
			Source: sourceOf("oidc", "enabled"), RestartRequired: restart},
		{Key: "oidc.issuer", Value: cfg.Issuer, Range: map[string]any{"kind": "string"},
			Source: sourceOf("oidc", "issuer"), RestartRequired: restart},
		{Key: "oidc.client_id", Value: cfg.ClientID, Range: map[string]any{"kind": "string"},
			Source: sourceOf("oidc", "client_id"), RestartRequired: restart},
		// Presence, never the value.
		{Key: "oidc.client_secret_set", Value: d.HasOIDCSecret,
			Source: sourceOf("oidc", "client_id"), RestartRequired: restart,
			ReadonlyReasonKey: readonly("settings.secret_is_write_only")},
		{Key: "oidc.scopes", Value: cfg.Scopes, Range: stringListRange(16),
			Source: sourceOf("oidc", "scopes"), RestartRequired: restart},
		{Key: "oidc.display_name", Value: rt.OIDCDisplayName, Range: map[string]any{"kind": "string"},
			Source: sourceOf("oidc", "display_name"), RestartRequired: restart},
		{Key: "oidc.allow_private_endpoints", Value: cfg.AllowPrivateEndpoints,
			Range:  map[string]any{"kind": "bool"},
			Source: sourceOf("oidc", "allow_private_endpoints"), RestartRequired: restart},
	}
}
