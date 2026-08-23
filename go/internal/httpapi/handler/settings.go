package handler

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
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

		const notInThisBuild = "settings.not_in_this_build"
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

			// Reported and not editable here. Each is a real value an operator
			// may need to see; the ones this build cannot move say so rather
			// than being left out, because a setting that is absent from the
			// screen is one nobody can find out the value of.
			{
				Key: "rate.per_sec", Value: d.Limiter.Rate(),
				Source: sourceConfig, ReadonlyReasonKey: readonly(notInThisBuild),
			},
			{
				Key: "rate.burst", Value: d.Limiter.Burst(),
				Source: sourceConfig, ReadonlyReasonKey: readonly(notInThisBuild),
			},
			{
				Key: "search.max_concurrent_fast", Value: limits.ConcurrentSearchesSSD,
				Range: intRange(1, 64), Source: sourceBuiltin,
				ReadonlyReasonKey: readonly(notInThisBuild),
			},
			{
				Key: "search.max_concurrent_slow", Value: limits.ConcurrentSearchesRotational,
				Range: intRange(1, 64), Source: sourceBuiltin,
				ReadonlyReasonKey: readonly(notInThisBuild),
			},
			{
				Key:   "search.walk_deadline_fast_ms",
				Value: limits.SearchWalkDeadlineSSD.Milliseconds(),
				Range: intRange(100, 60_000), Source: sourceBuiltin,
				ReadonlyReasonKey: readonly(notInThisBuild),
			},
			{
				Key:   "search.walk_deadline_slow_ms",
				Value: limits.SearchWalkDeadlineRotational.Milliseconds(),
				Range: intRange(100, 60_000), Source: sourceBuiltin,
				ReadonlyReasonKey: readonly(notInThisBuild),
			},
			{
				Key: "watch.hot_set_max", Value: d.WatchCap(),
				Range: intRange(64, 65_536), Source: sourceBuiltin,
				ReadonlyReasonKey: readonly(notInThisBuild),
			},
			{
				Key: "archive.max_concurrent", Value: limits.ArchiveEntriesListed,
				Source: sourceBuiltin, ReadonlyReasonKey: readonly(notInThisBuild),
			},
		}

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
		if req.AppHosts != nil || req.ContentHosts != nil {
			app := d.Hosts.App()
			content := d.Hosts.Content()
			if req.AppHosts != nil {
				if err := validateHosts(req.AppHosts); err != nil {
					return err
				}
				app = req.AppHosts
			}
			if req.ContentHosts != nil {
				if err := validateHosts(req.ContentHosts); err != nil {
					return err
				}
				content = req.ContentHosts
			}
			d.Hosts.Set(app, content)
		}
		return writeJSON(w, http.StatusOK, map[string]any{"applied": true})
	})
}

func validateHosts(hosts []string) error {
	for _, h := range hosts {
		if h == "" || strings.ContainsAny(h, " /\\") {
			return apierr.Unprocessable("settings.invalid_host", "app_hosts")
		}
	}
	return nil
}

func prefixesToStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}
