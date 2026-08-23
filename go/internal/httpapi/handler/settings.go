package handler

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
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
	// Source says where the live value came from, which is what the revert
	// affordance branches on.
	Source string `json:"source"`
	// RestartRequired is a property of the field, not of this value.
	RestartRequired bool `json:"restart_required"`
	// ReadonlyReasonKey names why a field cannot be changed here, or is null
	// when it can. A catalogue key rather than a sentence: the server does not
	// know which language the reader picked.
	ReadonlyReasonKey *string `json:"readonly_reason_key"`
}

// The sources a value can have.
const (
	sourceBuiltin  = "builtin_default"
	sourceConfig   = "config_file"
	sourceOverride = "admin_override"
)

// readonly marks a field this build reports and does not let the screen edit,
// with the reason as a catalogue key.
func readonly(key string) *string { return &key }

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
				return sourceConfig
			}
			if storedHas(r, d, section, key) {
				return sourceOverride
			}
			return sourceConfig
		}

		fields := []settingsField{
			// The network boundary, which is the one group this build moves
			// live: the host lists and the proxy ranges are what the settings
			// screen's network form patches.
			{
				Key: "app_hosts", Value: d.Hosts.App(),
				Range: stringListRange(32), Source: sourceConfig,
			},
			{
				Key: "content_hosts", Value: d.Hosts.Content(),
				Range: stringListRange(32), Source: sourceConfig,
			},
			{
				Key: "trusted_proxies", Value: prefixesToStrings(d.Trusted.Get()),
				Range: stringListRange(32), Source: sourceConfig,
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

		return writeJSON(w, http.StatusOK, map[string]any{
			"fields": fields,
			// No sidecar warning to raise when SMB is not published from here,
			// and false rather than absent because the screen draws a banner
			// off it either way.
			"smb_public_bind_warning": false,
		})
	})
}

// SettingsNetwork answers PATCH /api/admin/server-settings/network. The
// trusted-proxy ranges and the host lists are the one part of the proxy
// trust boundary an administrator may move live, so a malformed entry is
// refused here, where an administrator is watching and can fix it; the same
// entry is dropped with a warning at boot, because refusing there would make
// a server unbootable over a typo committed weeks ago.
func SettingsNetwork(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, aerr := requireAdmin(r, d.Auth); aerr != nil {
			return aerr
		}
		var req struct {
			TrustedProxyCIDRs []string `json:"trusted_proxy_cidrs,omitempty"`
			AppHosts          []string `json:"app_hosts,omitempty"`
			ContentHosts      []string `json:"content_hosts,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.TrustedProxyCIDRs != nil {
			parsed := make([]netip.Prefix, 0, len(req.TrustedProxyCIDRs))
			for _, cidr := range req.TrustedProxyCIDRs {
				p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
				if err != nil {
					return apierr.Unprocessable("settings.invalid_cidr", "trusted_proxy_cidrs")
				}
				parsed = append(parsed, p)
			}
			d.Trusted.Set(parsed)
		}
		// The same probes the preview and the generic save run. This route
		// exists because the network group has live holders to push into, not
		// because it has different rules, and a second copy of the rules here
		// is a second place they can disagree.
		probe := map[string]any{}
		if req.AppHosts != nil {
			probe["app_hosts"] = toAnyList(req.AppHosts)
		}
		if req.ContentHosts != nil {
			probe["content_hosts"] = toAnyList(req.ContentHosts)
		}
		if req.TrustedProxyCIDRs != nil {
			probe["trusted_proxies"] = toAnyList(req.TrustedProxyCIDRs)
		}
		if findings := checkSection(d, r, "network", probe); blocked(findings) {
			return settingsRefused(findings)
		}

		if req.AppHosts != nil || req.ContentHosts != nil {
			app := d.Hosts.App()
			content := d.Hosts.Content()
			if req.AppHosts != nil {
				app = req.AppHosts
			}
			if req.ContentHosts != nil {
				content = req.ContentHosts
			}
			d.Hosts.Set(app, content)
		}

		// Persisted as well as applied. Applying without storing is a boundary
		// that reverts on the next restart, which is the shape of change an
		// administrator does not find out about until something stops working.
		if d.State != nil {
			stored := map[string]any{}
			if req.AppHosts != nil {
				stored["app_hosts"] = req.AppHosts
			}
			if req.ContentHosts != nil {
				stored["content_hosts"] = req.ContentHosts
			}
			if req.TrustedProxyCIDRs != nil {
				stored["trusted_proxies"] = req.TrustedProxyCIDRs
			}
			if len(stored) > 0 {
				if err := d.State.MergeSettings(r.Context(), "network", stored); err != nil {
					return err
				}
			}
		}
		return writeJSON(w, http.StatusOK, applyOutcome("network", false))
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
			Key: "smb.config_dir", Value: d.SMBConfigDir, Source: sourceConfig,
			ReadonlyReasonKey: readonly("settings.readonly_smb_agent_socket"),
		},
	}
}
