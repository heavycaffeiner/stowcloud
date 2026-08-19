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

// Settings answers GET /api/admin/server-settings: the patchable bounds and
// the live values, so a client can show what is stored and what took effect.
func Settings(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, aerr := requireAdmin(r, d.Auth); aerr != nil {
			return aerr
		}
		return writeJSON(w, http.StatusOK, map[string]any{
			"rate_limit": map[string]any{
				"per_sec": d.Limiter.Rate(),
				"burst":   d.Limiter.Burst(),
			},
			"network": map[string]any{
				"trusted_proxy_cidrs": prefixesToStrings(d.Trusted.Get()),
				"app_hosts":           d.Hosts.App(),
				"content_hosts":       d.Hosts.Content(),
			},
			"search": map[string]any{
				"concurrency_ssd":        limits.ConcurrentSearchesSSD,
				"concurrency_rotational": limits.ConcurrentSearchesRotational,
				"walk_deadline_ssd":      limits.SearchWalkDeadlineSSD.String(),
				"walk_deadline_rot":      limits.SearchWalkDeadlineRotational.String(),
			},
			"watch": map[string]any{"hot_set_cap": d.WatchCap()},
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
