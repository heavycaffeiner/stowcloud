package runtimecfg

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
)

// The boot-time half: a document that was already validated at save.
//
// Malformed stored values are clamped or dropped with a logged warning, never
// refused. A server that will not start over one stale field is a server the
// emergency door has to fix, and the emergency door edits this same document,
// so graceful degradation at boot is what keeps the repair tool reachable.

// Store is the persistence side, implemented elsewhere. The settings table
// belongs to the store, and this package only ever reads a single document.
type Store interface {
	Settings(ctx context.Context) (map[string]any, error)
}

// Load reads the stored settings over the compiled-in defaults and returns what
// the server should run with.
//
// base supplies values for keys that were never saved. Every caller in the
// product passes Defaults.
func Load(ctx context.Context, st Store, base Values, log *slog.Logger) Values {
	if log == nil {
		log = slog.Default()
	}
	all, err := st.Settings(ctx)
	if err != nil {
		log.Warn("the stored settings could not be read; running with the compiled-in defaults",
			slog.Any("error", err))
		return base
	}

	doc := document{all: all, log: log}
	out := base
	loadSearch(doc, &out)
	loadWatchAndRate(doc, &out)
	loadNetwork(doc, &out)
	loadHomes(doc, &out)
	loadSecurity(doc, &out)
	loadDBGuard(doc, &out)
	loadOIDC(doc, &out)
	loadSMB(doc, &out)
	return out
}

// document is the stored settings as typed sections.
//
// The five near-identical map-spelunking helpers this replaces are gone: a
// caller names a section and a field and gets a typed answer, and the "absent
// leaves the base alone" rule lives in one place rather than five.
type document struct {
	all map[string]any
	log *slog.Logger
}

// section is one stored section, absent when nothing saved it.
func (d document) section(name string) (map[string]any, bool) {
	sec, ok := d.all[name].(map[string]any)
	return sec, ok
}

// has reports whether a section was saved at all, which is what distinguishes
// "configured and off" from "never configured".
func (d document) has(name string) bool {
	_, ok := d.section(name)
	return ok
}

// intOf reads one stored integer and clamps it into its bound.
//
// Absent leaves the caller's value alone, which is what makes the compiled-in
// default the floor rather than something the first save erases. JSON carries
// every number as a float, so any other shape is ignored rather than guessed
// at.
func (d document) intOf(section, key string, b Bound, set func(int64)) {
	sec, ok := d.section(section)
	if !ok {
		return
	}
	raw, ok := sec[key].(float64)
	if !ok {
		return
	}
	v := int64(raw)
	if c := b.Clamp(v); c != v {
		d.log.Warn("a stored setting is outside its bound and was clamped",
			slog.String("setting", section+"."+key),
			slog.Int64("stored", v), slog.Int64("using", c),
			slog.Int64("min", b.Min), slog.Int64("max", b.Max))
		v = c
	}
	set(v)
}

// uintOf reads one stored byte count. Negative and fractional are dropped: a
// size is neither.
func (d document) uintOf(section, key string, set func(uint64)) {
	sec, ok := d.section(section)
	if !ok {
		return
	}
	raw, ok := sec[key].(float64)
	if !ok || raw < 0 {
		return
	}
	set(uint64(raw))
}

func (d document) stringOf(section, key string, set func(string)) {
	sec, ok := d.section(section)
	if !ok {
		return
	}
	if v, ok := sec[key].(string); ok && v != "" {
		set(v)
	}
}

func (d document) boolOf(section, key string, set func(bool)) {
	if v, ok := d.rawBool(section, key); ok {
		set(v)
	}
}

// rawBool is boolOf for a caller that has to branch on the value rather than
// hand it somewhere.
func (d document) rawBool(section, key string) (bool, bool) {
	sec, ok := d.section(section)
	if !ok {
		return false, false
	}
	v, ok := sec[key].(bool)
	return v, ok
}

// stringsOf reads one stored list. A member of the wrong shape is dropped
// rather than taking the list down with it, and an empty result is not applied:
// every one of these is a boundary, and a stored empty would silently widen or
// close it depending on which.
func (d document) stringsOf(section, key string, set func([]string)) {
	sec, ok := d.section(section)
	if !ok {
		return
	}
	raw, ok := sec[key].([]any)
	if !ok {
		return
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		set(out)
	}
}

// validStrings is stringsOf with a per-entry check, dropping what will not pass
// with a line rather than refusing the start. The same value is refused at save
// time, where somebody is watching.
func (d document) validStrings(section, key string, check func(string) error, set func([]string)) {
	d.stringsOf(section, key, func(in []string) {
		out := make([]string, 0, len(in))
		for _, v := range in {
			if err := check(v); err != nil {
				d.log.Warn("a stored entry will not parse and was dropped",
					slog.String("setting", section+"."+key),
					slog.String("entry", v), slog.Any("error", err))
				continue
			}
			out = append(out, v)
		}
		if len(out) > 0 {
			set(out)
		}
	})
}

func loadSearch(d document, out *Values) {
	d.intOf("search", "max_concurrent_fast", BoundSearchConcurrent(), func(v int64) {
		out.SearchConcurrentSSD = int(v)
	})
	d.intOf("search", "max_concurrent_slow", BoundSearchConcurrent(), func(v int64) {
		out.SearchConcurrentRot = int(v)
	})
	d.intOf("search", "walk_deadline_fast_ms", BoundSearchDeadlineMs(), func(v int64) {
		out.SearchDeadlineSSD = time.Duration(v) * time.Millisecond
	})
	d.intOf("search", "walk_deadline_slow_ms", BoundSearchDeadlineMs(), func(v int64) {
		out.SearchDeadlineRot = time.Duration(v) * time.Millisecond
	})
	d.intOf("archive", "max_concurrent", BoundArchiveEntries(), func(v int64) {
		out.ArchiveMaxConcurrent = int(v)
	})
}

func loadWatchAndRate(d document, out *Values) {
	d.intOf("watch", "hot_set_max", BoundWatchHotSet(), func(v int64) {
		out.WatchHotSetMax = int(v)
	})
	d.intOf("watch", "full_threshold", BoundWatchFullThreshold(), func(v int64) {
		out.WatchFullThreshold = int(v)
	})
	d.intOf("rate", "per_sec", BoundRatePerSec(), func(v int64) {
		out.RatePerSec = float64(v)
	})
	d.intOf("rate", "burst", BoundRateBurst(), func(v int64) {
		out.RateBurst = int(v)
	})
}

// loadNetwork reads the boundary. An entry that cannot be parsed is dropped at
// boot with a line rather than refusing the start.
func loadNetwork(d document, out *Values) {
	d.validStrings("network", "app_hosts", CheckHost, func(v []string) { out.AppHosts = v })
	d.validStrings("network", "content_hosts", CheckHost, func(v []string) { out.ContentHosts = v })
	d.validStrings("network", "trusted_proxies", CheckCIDR, func(v []string) { out.TrustedProxy = v })
	d.validStrings("network", "allowed_origins", CheckOrigin, func(v []string) { out.AllowedOrigins = v })

	// A host claimed by both roles is dropped from the content list rather than
	// refusing the start: the app role is what carries the session, so it is
	// the one that keeps the name.
	if overlap := dropOverlap(out); len(overlap) > 0 {
		d.log.Warn("a stored host is listed as both an app host and a content host, so it was dropped from the content list",
			slog.Any("hosts", overlap))
	}

	d.stringOf("network", "compat_canonical_url", func(v string) {
		if err := CheckCanonicalURL(v, out.AppHosts); err != nil {
			d.log.Warn("the stored canonical URL does not name a declared app host; leaving it unset",
				slog.String("stored", v), slog.Any("error", err))
			return
		}
		out.CompatCanonicalURL = v
	})

	// The listener is dropped rather than clamped when it will not parse: an
	// address nothing can bind is a server that does not come up.
	d.stringOf("network", "bind", func(v string) {
		if err := CheckListen(v); err != nil {
			d.log.Warn("the stored bind address will not parse; using the default",
				slog.String("stored", v), slog.String("using", out.Listen), slog.Any("error", err))
			return
		}
		out.Listen = v
	})
}

// dropOverlap removes from the content list every host the app list already
// claims, and reports what it dropped.
func dropOverlap(out *Values) []string {
	if len(out.ContentHosts) == 0 || len(out.AppHosts) == 0 {
		return nil
	}
	app := make(map[string]struct{}, len(out.AppHosts))
	for _, h := range out.AppHosts {
		app[strings.ToLower(h)] = struct{}{}
	}
	kept := make([]string, 0, len(out.ContentHosts))
	var dropped []string
	for _, h := range out.ContentHosts {
		if _, clash := app[strings.ToLower(h)]; clash {
			dropped = append(dropped, h)
			continue
		}
		kept = append(kept, h)
	}
	out.ContentHosts = kept
	return dropped
}

func loadHomes(d document, out *Values) {
	d.boolOf("homes", "enabled", func(v bool) { out.HomesEnabled = v })
	d.stringOf("homes", "root", func(v string) { out.HomesRoot = v })
}

func loadSecurity(d document, out *Values) {
	d.stringOf("security", "hardening", func(v string) {
		pol, err := jail.ParsePolicy(v)
		if err != nil {
			d.log.Warn("the stored hardening policy is not one this build has; using the default",
				slog.String("stored", v), slog.Any("error", err))
			return
		}
		out.Hardening = pol
	})
}

// loadDBGuard reads the size guard. The switch is what applies the bounds, so
// the numbers can be set before it is turned on.
func loadDBGuard(d document, out *Values) {
	on, ok := d.rawBool("db", "size_guard")
	if !ok || !on {
		return
	}
	d.uintOf("db", "max_bytes", func(v uint64) { out.DBGuard.MaxBytes = v })
	d.uintOf("db", "min_free_bytes", func(v uint64) { out.DBGuard.MinFreeBytes = v })
	if !out.DBGuard.Enabled() {
		d.log.Warn("the store's size guard is on and neither bound is set, so nothing would trip it; leaving it off")
	}
}

// loadOIDC reads the provider settings, returning nil instead of a disabled
// client. A present-but-refusing client forces every caller to remember to
// interrogate it first.
//
// Incomplete is off with a line, not a refused start: single sign-on missing is
// a deployment where people use passwords, and a server that will not boot is a
// deployment where nobody signs in at all.
func loadOIDC(d document, out *Values) {
	on, ok := d.rawBool("oidc", "enabled")
	if !ok || !on {
		return
	}
	c := OIDC{Enabled: true}
	d.stringOf("oidc", "issuer", func(v string) { c.Issuer = v })
	d.stringOf("oidc", "client_id", func(v string) { c.ClientID = v })
	d.stringOf("oidc", "ca_cert_file", func(v string) { c.CACertFile = v })
	d.stringsOf("oidc", "scopes", func(v []string) { c.Scopes = v })
	d.boolOf("oidc", "allow_private_endpoints", func(v bool) { c.AllowPrivateEndpoints = v })

	if c.Issuer == "" || c.ClientID == "" {
		d.log.Error("single sign-on is on and has no issuer or client id; it stays off")
		return
	}
	out.OIDC = &c
	out.OIDCDisplayName = "Single sign-on"
	d.stringOf("oidc", "display_name", func(v string) { out.OIDCDisplayName = v })
}

// loadSMB reads the sidecar's settings whole: a render takes a configuration
// and produces four files, so a half-applied change is a configuration nobody
// wrote.
func loadSMB(d document, out *Values) {
	if !d.has("smb") {
		return
	}
	out.SMBConfigured = true

	d.boolOf("smb", "enabled", func(v bool) { out.SMB.Enabled = v })
	d.stringOf("smb", "workgroup", func(v string) { out.SMB.Workgroup = v })
	d.stringOf("smb", "server_name", func(v string) { out.SMB.ServerName = v })
	d.stringOf("smb", "service_user", func(v string) { out.SMB.ServiceUser = v })
	d.boolOf("smb", "allow_public_bind", func(v bool) { out.SMB.AllowPublicBind = v })
	d.stringsOf("smb", "interfaces", func(v []string) { out.SMB.Interfaces = v })
	d.stringOf("smb", "totp_policy", func(v string) { out.SMBTOTPPolicy = v })
	d.stringOf("smb", "config_dir", func(v string) { out.SMBConfigDir = v })
	d.stringOf("smb", "agent_socket", func(v string) { out.SMBSocket = v })
	d.intOf("smb", "service_gid", BoundServiceGID(), func(v int64) {
		// The bound above excludes zero and caps at the width, which is what
		// makes this fit.
		out.SMBServiceGID = uint32(v) //nolint:gosec // G115: bounded on the line above.
	})

	// Settings that cannot be rendered are discarded here rather than carried
	// forward to the first publish. SMB is left off with the reason logged,
	// yielding a deployment that serves no SMB shares instead of one that fails
	// to start.
	if out.SMB.Enabled {
		if err := smb.Validate(out.SMB); err != nil {
			d.log.Error("the stored SMB settings cannot be rendered; SMB stays off",
				slog.Any("error", err))
			out.SMB.Enabled = false
		}
	}
}
