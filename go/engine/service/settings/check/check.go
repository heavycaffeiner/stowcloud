//go:build linux

package check

import (
	"errors"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
)

// The reason keys. Named constants rather than inline strings, because the
// client renders them and a typo here is a message that renders as its own key.
const (
	keyMustBeAtLeastOne    = "settings.must_be_at_least_one"
	keyOutOfRange          = "settings.out_of_range"
	keyCheckPassed         = "settings.check_passed"
	keyUnknownHardening    = "settings.unknown_hardening_policy"
	keyGuardHasNoBound     = "settings.guard_has_no_bound"
	keyRequiredWhenEnabled = "settings.required_when_enabled"
	keyIssuerMustBeHTTPS   = "settings.issuer_must_be_https"
	keyHostListEmpty       = "settings.host_list_empty"
	keyInvalidHost         = "settings.invalid_host"
	keyHostRoleConflict    = "settings.host_role_conflict"
	keyDuplicateHost       = "settings.duplicate_host"
	keyInvalidOrigin       = "settings.invalid_origin"
	keyCanonicalNotAppHost = "settings.canonical_url_not_an_app_host"
	keyInvalidBindAddress  = "settings.invalid_bind_address"
	keyWouldLockYouOut     = "settings.would_lock_you_out"
	keyInvalidCIDR         = "settings.invalid_cidr"
	keyProxyIsEverything   = "settings.proxy_range_is_everything"
	keyPathMustBeAbsolute  = "settings.path_must_be_absolute"
	keyDirDoesNotExist     = "settings.dir_does_not_exist"
	keyDirNotWritable      = "settings.dir_not_writable"
	keyDirWillBeCreated    = "settings.dir_will_be_created"
	keyDirIsWritable       = "settings.dir_is_writable"
	keyPathIsNotADirectory = "settings.path_is_not_a_directory"
	keyUnknownTOTPPolicy   = "settings.unknown_totp_policy"
	keyGIDZeroIsRoot       = "settings.gid_zero_is_root"
	keySMBRenderFailed     = "settings.smb_render_failed"
	keySMBDirUnavailable   = "settings.smb_config_dir_unavailable"
	keyAboveWatchLimit     = "settings.above_kernel_watch_limit"
	keyWithinWatchLimit    = "settings.within_kernel_watch_limit"
)

// watchLimitFile is where the kernel reports what it will actually grant. It is
// host-boundary input: a read failure or garbage skips the check rather than
// blocking a save, because a missing proc file is not the administrator's
// mistake.
const watchLimitFile = "/proc/sys/fs/inotify/max_user_watches"

// Section runs every probe that applies to one section's proposed values.
func Section(in Input) []Finding {
	out := checkBounds(in)

	switch in.Section {
	case "network":
		out = append(out, checkNetwork(in)...)
	case "homes":
		out = append(out, checkHomes(in)...)
	case "smb":
		out = append(out, checkSMB(in)...)
	case "watch":
		out = append(out, checkWatch(in)...)
	case "security":
		out = append(out, checkSecurity(in)...)
	case "db":
		out = append(out, checkDB(in)...)
	case "oidc":
		out = append(out, checkOIDC(in)...)
	}
	if len(out) == 0 {
		out = append(out, advisory(in.Section, "", keyCheckPassed))
	}
	return out
}

// checkBounds runs the numeric bounds first, so a value that cannot be stored
// is reported beside the ones that can.
//
// The bounds come from runtimecfg's own table, so the screen, the checker and
// the loader cannot disagree about what is acceptable.
func checkBounds(in Input) []Finding {
	var out []Finding
	bounds := runtimecfg.Bounds()

	for key, raw := range in.Body {
		b, governed := bounds[in.Section+"."+key]
		if !governed {
			continue
		}
		n, ok := raw.(float64)
		if !ok {
			out = append(out, blocking(in.Section, key, keyMustBeAtLeastOne, "field", key))
			continue
		}
		if err := runtimecfg.Check(key, int64(n), b); err != nil {
			out = append(out, blocking(in.Section, key, keyOutOfRange,
				"field", key,
				"min", strconv.FormatInt(b.Min, 10),
				"max", strconv.FormatInt(b.Max, 10)))
		}
	}
	return out
}

// checkSecurity rejects a hardening policy name this build does not implement.
//
// Rejection beats substitution here. Startup falls back to the shipped policy,
// so quietly accepting an unknown name would hand the operator a different
// policy than the one requested, in either direction, without telling them.
func checkSecurity(in Input) []Finding {
	v, ok := in.Body["hardening"].(string)
	if !ok || v == "" {
		return nil
	}
	if _, err := jail.ParsePolicy(v); err != nil {
		return []Finding{blocking(in.Section, "hardening", keyUnknownHardening, "value", v)}
	}
	return nil
}

// checkDB rejects a size guard that can never fire.
//
// Both limits are inert until the switch is on, so enabling it while leaving
// each limit unset yields a control that appears to bound the volume and does
// nothing at all.
func checkDB(in Input) []Finding {
	on, ok := in.Body["size_guard"].(bool)
	if !ok || !on {
		return nil
	}
	maxBytes := numberOr(in.Body, "max_bytes", 0)
	minFree := numberOr(in.Body, "min_free_bytes", 0)
	if maxBytes <= 0 && minFree <= 0 {
		return []Finding{blocking(in.Section, "size_guard", keyGuardHasNoBound)}
	}
	return nil
}

// checkOIDC rejects a provider configuration that cannot complete a sign-in.
//
// Enabling it without an issuer or client id ships a sign-in button that fails
// for every user who tries it. Refusing the save surfaces the problem to the
// one administrator who can still fix it.
func checkOIDC(in Input) []Finding {
	on, ok := in.Body["enabled"].(bool)
	if !ok || !on {
		return nil
	}
	var out []Finding
	for _, key := range []string{"issuer", "client_id"} {
		if v, ok := in.Body[key].(string); !ok || strings.TrimSpace(v) == "" {
			out = append(out, blocking(in.Section, key, keyRequiredWhenEnabled, "field", key))
		}
	}
	if v, ok := in.Body["issuer"].(string); ok && v != "" {
		if u, err := url.Parse(v); err != nil || u.Scheme != "https" || u.Host == "" {
			out = append(out, blocking(in.Section, "issuer", keyIssuerMustBeHTTPS, "value", v))
		}
	}
	return out
}

// checkNetwork probes the host roles, the origins, the listener and the proxy
// ranges.
//
// The lockout probe is the reason this function is worth its length. A list
// omitting the administrator's current host applies at once, and every later
// request, the corrective one included, is then turned away by a server that
// is otherwise running normally.
func checkNetwork(in Input) []Finding {
	var out []Finding

	appHosts, appPresent, appErr := stringList(in.Body, "app_hosts")
	contentHosts, _, contentErr := stringList(in.Body, "content_hosts")

	if appErr != nil {
		out = append(out, blocking(in.Section, "app_hosts", keyMustBeAtLeastOne, "field", "app_hosts"))
	}
	if contentErr != nil {
		out = append(out, blocking(in.Section, "content_hosts", keyMustBeAtLeastOne, "field", "content_hosts"))
	}
	if appPresent && len(appHosts) == 0 {
		out = append(out, blocking(in.Section, "app_hosts", keyHostListEmpty))
	}

	out = append(out, checkHostRoles(in, appHosts, contentHosts)...)
	out = append(out, checkOrigins(in, appHosts)...)
	out = append(out, checkBind(in)...)
	out = append(out, checkLockout(in, appHosts, appPresent)...)
	out = append(out, checkProxies(in)...)
	return out
}

// checkHostRoles validates the two lists as two roles. A host in both is
// blocking: one TLS name cannot both carry the session application and be the
// cookie-free content origin.
func checkHostRoles(in Input, appHosts, contentHosts []string) []Finding {
	var out []Finding

	// The role a name is already claimed by, so a second sighting can say
	// whether it is a repeat within one list or a conflict across the two.
	claimed := make(map[string]string, len(appHosts)+len(contentHosts))

	for _, role := range []struct {
		field string
		hosts []string
	}{
		{"app_hosts", appHosts},
		{"content_hosts", contentHosts},
	} {
		for _, h := range role.hosts {
			if err := runtimecfg.CheckHost(h); err != nil {
				out = append(out, blocking(in.Section, role.field, keyInvalidHost,
					"value", h, "field", role.field))
				continue
			}
			key := strings.ToLower(strings.TrimSpace(h))
			switch other, seen := claimed[key]; {
			case !seen:
				claimed[key] = role.field
			case other == role.field:
				out = append(out, blocking(in.Section, role.field, keyDuplicateHost,
					"value", h, "field", role.field))
			default:
				out = append(out, blocking(in.Section, role.field, keyHostRoleConflict,
					"value", h, "field", role.field, "other_field", other))
			}
		}
	}
	return out
}

// checkOrigins validates the CORS list and the canonical fallback.
//
// Neither can widen the host guard: these validate roles rather than folding
// every host-like string into one allowlist.
func checkOrigins(in Input, appHosts []string) []Finding {
	var out []Finding
	if origins, present, err := stringList(in.Body, "allowed_origins"); err == nil && present {
		for _, o := range origins {
			if oerr := runtimecfg.CheckOrigin(o); oerr != nil {
				out = append(out, blocking(in.Section, "allowed_origins", keyInvalidOrigin, "value", o))
			}
		}
	}
	if v, ok := in.Body["compat_canonical_url"].(string); ok && v != "" {
		if err := runtimecfg.CheckCanonicalURL(v, appHosts); err != nil {
			out = append(out, blocking(in.Section, "compat_canonical_url",
				keyCanonicalNotAppHost, "value", v))
		}
	}
	return out
}

// checkBind refuses a bind address nothing can bind, here where somebody is
// watching rather than at the next start.
func checkBind(in Input) []Finding {
	v, ok := in.Body["bind"].(string)
	if !ok || v == "" {
		return nil
	}
	if err := runtimecfg.CheckListen(v); err != nil {
		return []Finding{blocking(in.Section, "bind", keyInvalidBindAddress, "value", v)}
	}
	return nil
}

// checkLockout is the probe this file exists for. Only the app host list serves
// the screen, so only that one can strand the person saving it.
func checkLockout(in Input, appHosts []string, present bool) []Finding {
	if !present || len(appHosts) == 0 || in.SelfHost == "" || hostAllowed(appHosts, in.SelfHost) {
		return nil
	}
	f := blocking(in.Section, "app_hosts", keyWouldLockYouOut, "host", in.SelfHost)
	if in.Lockout == LockoutWarns {
		f.Blocking = false
	}
	return []Finding{f}
}

func checkProxies(in Input) []Finding {
	cidrs, present, err := stringList(in.Body, "trusted_proxies")
	if err != nil || !present {
		return nil
	}
	var out []Finding
	for _, c := range cidrs {
		p, perr := netip.ParsePrefix(strings.TrimSpace(c))
		if perr != nil {
			out = append(out, blocking(in.Section, "trusted_proxies", keyInvalidCIDR, "value", c))
			continue
		}
		// A range covering everything means any client can claim any address
		// through a forwarded header, which decides what the rate limiter
		// counts and what the audit log records.
		if p.Bits() == 0 {
			out = append(out, advisory(in.Section, "trusted_proxies", keyProxyIsEverything, "value", c))
		}
	}
	return out
}

// hostAllowed applies the guard's own matching rule: case-insensitive equality,
// with no pattern syntax of any kind.
//
// Matching more loosely than the guard would let a list pass here and still be
// refused at runtime, causing exactly the lockout this probe is meant to catch.
func hostAllowed(list []string, host string) bool {
	for _, h := range list {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true
		}
	}
	return false
}

// HostOnly reduces a host:port pair to the lowercased host, the form host list
// comparisons use.
func HostOnly(hostport string) string {
	h := hostport
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// checkHomes examines the directory that would back the homes share.
//
// Enabling homes publishes a share under which each account receives its own
// folder. If the root cannot be written, the feature still appears in the
// interface and breaks for whoever opens it first.
func checkHomes(in Input) []Finding {
	root := homesRoot(in)
	if root == "" {
		return nil
	}
	if !filepath.IsAbs(root) {
		return []Finding{blocking(in.Section, "root", keyPathMustBeAbsolute)}
	}

	// The probe writes rather than inspecting metadata: existence and actual
	// write access are separate questions, and only write access decides
	// whether the share will work.
	info, err := os.Stat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return probeParent(in, root)
	case err != nil:
		return []Finding{blocking(in.Section, "root", keyDirNotWritable,
			"field", "root", "path", root, "error", err.Error())}
	case !info.IsDir():
		return []Finding{blocking(in.Section, "root", keyPathIsNotADirectory, "path", root)}
	}
	if werr := probeWritable(root); werr != nil {
		return []Finding{blocking(in.Section, "root", keyDirNotWritable,
			"field", "root", "path", root, "error", werr.Error())}
	}
	return []Finding{advisory(in.Section, "root", keyDirIsWritable, "path", root)}
}

// homesRoot is the directory the probe should look at, which is the named one
// or the default under the data directory when homes are being turned on.
func homesRoot(in Input) string {
	if v, ok := in.Body["root"].(string); ok && v != "" {
		return v
	}
	enabled, ok := in.Body["enabled"].(bool)
	if !ok || !enabled || in.DataDir == "" {
		return ""
	}
	return filepath.Join(in.DataDir, "homes")
}

// probeParent answers for a root that does not exist yet: it can be created
// when its parent takes a file.
func probeParent(in Input, root string) []Finding {
	parent := filepath.Dir(root)
	if _, err := os.Stat(parent); err != nil {
		return []Finding{blocking(in.Section, "root", keyDirDoesNotExist,
			"field", "root", "path", parent)}
	}
	if err := probeWritable(parent); err != nil {
		return []Finding{blocking(in.Section, "root", keyDirNotWritable,
			"field", "root", "path", parent, "error", err.Error())}
	}
	return []Finding{advisory(in.Section, "root", keyDirWillBeCreated, "path", root)}
}

// probeWritable creates a temporary file and deletes it. Mode bits alone are
// not sufficient evidence, since a read-only mount or a restrictive ACL can
// both leave permissive-looking bits in place.
//
// Removal is attempted regardless of the close result, and a close failure is
// reported ahead of a removal failure: it is the more direct statement about
// the directory, and the probe must not leave files behind in the homes root.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".sc-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	cerr := f.Close()
	rerr := os.Remove(name)
	if cerr != nil {
		return cerr
	}
	return rerr
}

// checkSMB validates the configuration that would be published.
//
// Through the one dry-validate entry point rather than a second copy of the
// renderer's defaults, which is the only way the preview and the publish cannot
// disagree.
func checkSMB(in Input) []Finding {
	var out []Finding
	if v, ok := in.Body["totp_policy"].(string); ok && v != "require_separate" && v != "block" {
		out = append(out, blocking(in.Section, "totp_policy", keyUnknownTOTPPolicy, "value", v))
	}
	if v, ok := in.Body["service_gid"].(float64); ok && v == 0 {
		out = append(out, blocking(in.Section, "service_gid", keyGIDZeroIsRoot))
	}

	cfg := smb.Config{Enabled: true}
	if v, ok := in.Body["workgroup"].(string); ok && v != "" {
		cfg.Workgroup = v
	}
	if v, ok := in.Body["server_name"].(string); ok {
		cfg.ServerName = v
	}
	if v, ok := in.Body["service_user"].(string); ok && v != "" {
		cfg.ServiceUser = v
	}
	if err := smb.Validate(cfg); err != nil {
		out = append(out, blocking(in.Section, "workgroup", keySMBRenderFailed, "error", err.Error()))
	}

	return append(out, probeSMBDir(in)...)
}

// probeSMBDir is only worth running when SMB is being turned on: the directory
// is the sidecar's, and its absence is expected on a server not running one.
func probeSMBDir(in Input) []Finding {
	on, ok := in.Body["enabled"].(bool)
	if !ok || !on || in.SMBConfigDir == "" {
		return nil
	}
	if _, err := os.Stat(in.SMBConfigDir); err != nil {
		return []Finding{advisory(in.Section, "enabled", keySMBDirUnavailable,
			"path", in.SMBConfigDir, "error", err.Error())}
	}
	if err := probeWritable(in.SMBConfigDir); err != nil {
		return []Finding{advisory(in.Section, "enabled", keySMBDirUnavailable,
			"path", in.SMBConfigDir, "error", err.Error())}
	}
	return nil
}

// checkWatch weighs the requested hot set against the allowance the kernel
// will actually grant.
//
// Requesting more watches than the kernel permits is accepted by the interface
// but never fulfilled. The symptom appears much later, as edits that stay
// invisible until some unrelated event forces a rescan.
func checkWatch(in Input) []Finding {
	want, ok := in.Body["hot_set_max"].(float64)
	if !ok {
		return nil
	}
	limit, ok := kernelWatchLimit()
	if !ok {
		// A missing or garbled proc file is not the administrator's mistake, so
		// the check is skipped rather than blocking the save.
		return nil
	}
	if int64(want) > int64(limit) {
		return []Finding{advisory(in.Section, "hot_set_max", keyAboveWatchLimit,
			"value", strconv.FormatInt(int64(want), 10),
			"limit", strconv.Itoa(limit))}
	}
	return []Finding{advisory(in.Section, "hot_set_max", keyWithinWatchLimit,
		"limit", strconv.Itoa(limit))}
}

// kernelWatchLimit reads what the kernel reports, treating anything it cannot
// parse as absent.
func kernelWatchLimit() (int, bool) {
	raw, err := os.ReadFile(watchLimitFile)
	if err != nil {
		return 0, false
	}
	return parseWatchLimit(string(raw))
}

// parseWatchLimit is the boundary: the proc file is host input, and anything
// that is not one positive number means the kernel did not answer.
func parseWatchLimit(raw string) (int, bool) {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit <= 0 {
		return 0, false
	}
	return limit, true
}

// stringList reads a single list field. A field present with the wrong shape is
// an error; a missing field is not, since a patch only names what it modifies.
func stringList(body map[string]any, key string) ([]string, bool, error) {
	raw, present := body[key]
	if !present {
		return nil, false, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false, errors.New("the value is not a list")
	}
	out := make([]string, 0, len(items))
	for _, v := range items {
		s, ok := v.(string)
		if !ok {
			return nil, false, errors.New("a member of the list is not a string")
		}
		out = append(out, s)
	}
	return out, true, nil
}

// numberOr reads one stored number, treating absent and wrong-typed alike:
// both mean the field was not named.
func numberOr(body map[string]any, key string, fallback float64) float64 {
	if v, ok := body[key].(float64); ok {
		return v
	}
	return fallback
}
