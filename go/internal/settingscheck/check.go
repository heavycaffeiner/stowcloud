// Linux only: it names the sandbox policies and the SMB renderer.
//go:build linux

// Package settingscheck probes a proposed settings change by trying it,
// rather than by describing it.
//
// A range in the interface says what a number may be. It does not say whether
// the directory exists, whether the renderer accepts the workgroup name, or
// whether the host list an administrator is about to save still contains the
// host they are typing into. Those are answered by looking, and looking is
// what this package does.
//
// It is its own package because three surfaces run the same probes: the
// settings screen, the first-run form, and the emergency editor. The third one
// runs in a process that may have no engine at all, so the probes cannot live
// beside the handlers that reach into it.
package settingscheck

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
)

// Finding is one thing learned by probing.
type Finding struct {
	// Level is "block", "warn" or "ok". A block refuses the save. A warning
	// is saved and reported, because plenty of true statements about a
	// configuration are not reasons to refuse it.
	Level string `json:"level"`
	// Field is the input to put the message beside, empty when the finding is
	// about the section as a whole.
	Field        string            `json:"field,omitempty"`
	ReasonKey    string            `json:"reason_key"`
	ReasonParams map[string]string `json:"reason_params,omitempty"`
}

// The three levels, named so a caller comparing against them is not writing
// the string a fourth time.
const (
	LevelBlock = "block"
	LevelWarn  = "warn"
	LevelOK    = "ok"
)

// Lockout decides what a host list that omits the current host does.
//
// It is a parameter rather than a rule because the answer depends on which
// screen is asking, and the difference is load-bearing in both directions.
type Lockout int

const (
	// LockoutBlocks refuses the save. This is the settings screen: the guard
	// is live, so the next request, including the one that would undo the
	// change, is the one that gets refused.
	LockoutBlocks Lockout = iota
	// LockoutWarns saves and reports. It is for the two screens the guard does
	// not gate. The first-run form is one: an operator browsing by address
	// while naming the DNS name the deployment will be reached under is doing
	// the right thing, and no rule can tell that apart from the mistake. The
	// emergency editor is the other: it is where somebody goes to repair a
	// host list that already locked them out, so refusing over the host they
	// are on would refuse every repair.
	LockoutWarns
)

// Input is one proposed change, with the surroundings the probes look at.
type Input struct {
	Section string
	Body    map[string]any

	// SelfHost is the host the caller is reaching this server under, without
	// the port. It is what the lockout probe compares the new list against.
	// Empty skips that probe.
	SelfHost string

	// DataDir resolves the homes root when the request did not name one.
	DataDir string
	// SMBConfigDir is the directory the sidecar mounts, probed for writability
	// when SMB is being turned on. Empty skips that probe.
	SMBConfigDir string

	Lockout Lockout
}

func blocking(field, key string, params map[string]string) Finding {
	return Finding{Level: LevelBlock, Field: field, ReasonKey: key, ReasonParams: params}
}

func warning(field, key string, params map[string]string) Finding {
	return Finding{Level: LevelWarn, Field: field, ReasonKey: key, ReasonParams: params}
}

func noted(field, key string, params map[string]string) Finding {
	return Finding{Level: LevelOK, Field: field, ReasonKey: key, ReasonParams: params}
}

// Blocked reports whether anything in the list refuses the save.
func Blocked(findings []Finding) bool {
	for _, f := range findings {
		if f.Level == LevelBlock {
			return true
		}
	}
	return false
}

// Warnings is what was noticed and not refused over, which is what a screen
// shows after a save it let through.
func Warnings(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if f.Level == LevelWarn {
			out = append(out, f)
		}
	}
	return out
}

// Section probes one section's proposed values.
func Section(in Input) []Finding {
	var out []Finding
	// The numeric bounds first, so a value that cannot be stored is reported
	// beside the ones that can.
	for key, b := range Bounds()[in.Section] {
		raw, ok := in.Body[key]
		if !ok {
			continue
		}
		n, ok := raw.(float64)
		if !ok {
			out = append(out, blocking(key, "settings.must_be_at_least_one", nil))
			continue
		}
		if err := runtimecfg.Check(key, int64(n), b); err != nil {
			out = append(out, blocking(key, "settings.out_of_range", map[string]string{
				"field": key,
				"min":   strconv.FormatInt(b.Min, 10),
				"max":   strconv.FormatInt(b.Max, 10),
			}))
		}
	}

	switch in.Section {
	case "network":
		out = append(out, checkNetwork(in)...)
	case "homes":
		out = append(out, checkHomes(in)...)
	case "smb":
		out = append(out, checkSMB(in)...)
	case "watch":
		out = append(out, checkWatch(in.Body)...)
	case "security":
		out = append(out, checkSecurity(in.Body)...)
	case "db":
		out = append(out, checkDB(in.Body)...)
	case "oidc":
		out = append(out, checkOIDC(in.Body)...)
	}
	if len(out) == 0 {
		out = append(out, noted("", "settings.check_passed", nil))
	}
	return out
}

// checkSecurity refuses a hardening policy this build does not have.
//
// Refused here rather than clamped, because the boot-time read falls back to
// the shipped policy and an administrator who asked for a weaker one would
// otherwise get a stronger one with no explanation.
func checkSecurity(body map[string]any) []Finding {
	v, ok := body["hardening"].(string)
	if !ok || v == "" {
		return nil
	}
	if _, err := jail.ParsePolicy(v); err != nil {
		return []Finding{blocking("hardening", "settings.unknown_hardening_policy",
			map[string]string{"value": v})}
	}
	return nil
}

// checkDB refuses a size guard that cannot trip.
//
// The switch is what applies the bounds, so turning it on with neither one set
// is a control that reads as protecting the volume and protects nothing.
func checkDB(body map[string]any) []Finding {
	on, ok := body["size_guard"].(bool)
	if !ok || !on {
		return nil
	}
	maxBytes, _ := body["max_bytes"].(float64)     //nolint:errcheck // absent and wrong-typed both mean unset.
	minFree, _ := body["min_free_bytes"].(float64) //nolint:errcheck // as above.
	if maxBytes <= 0 && minFree <= 0 {
		return []Finding{blocking("size_guard", "settings.guard_has_no_bound", nil)}
	}
	return nil
}

// checkOIDC refuses a provider that cannot be reached.
//
// Turning single sign-on on without an issuer or a client id produces a button
// that fails for whoever presses it, which is a worse answer than refusing the
// save.
func checkOIDC(body map[string]any) []Finding {
	on, ok := body["enabled"].(bool)
	if !ok || !on {
		return nil
	}
	var out []Finding
	for _, key := range []string{"issuer", "client_id"} {
		if v, ok := body[key].(string); !ok || strings.TrimSpace(v) == "" {
			out = append(out, blocking(key, "settings.required_when_enabled",
				map[string]string{"field": key}))
		}
	}
	if v, ok := body["issuer"].(string); ok && v != "" {
		if u, err := url.Parse(v); err != nil || u.Scheme != "https" || u.Host == "" {
			out = append(out, blocking("issuer", "settings.issuer_must_be_https",
				map[string]string{"value": v}))
		}
	}
	return out
}

// checkNetwork probes the host lists, the listener and the proxy ranges.
//
// The finding that earns this file is the lockout: a host list that does not
// contain the host the administrator is on takes effect immediately and makes
// the next request, including the one that would undo it, answer "misdirected
// request" from a server that is otherwise healthy.
func checkNetwork(in Input) []Finding {
	var out []Finding
	body := in.Body

	hosts, present, err := stringList(body, "app_hosts")
	switch {
	case err != nil:
		out = append(out, blocking("app_hosts", "settings.must_be_at_least_one", nil))
	case present && len(hosts) == 0:
		out = append(out, blocking("app_hosts", "settings.host_list_empty", nil))
	case present:
		for _, h := range hosts {
			if herr := runtimecfg.CheckHost(h); herr != nil {
				out = append(out, blocking("app_hosts", "settings.invalid_host",
					map[string]string{"value": h}))
			}
		}
	}

	// The listener. It is saved in the same section as the host list, and a
	// bind address nothing can bind is a server that does not come up: refused
	// here, where somebody is watching, rather than dropped at the next start.
	if v, ok := body["bind"].(string); ok && v != "" {
		if berr := runtimecfg.CheckListen(v); berr != nil {
			out = append(out, blocking("bind", "settings.invalid_bind_address",
				map[string]string{"value": v}))
		}
	}

	// The lockout probe. Only the app host list serves this screen, so only
	// that one can strand the person saving it.
	if present && len(hosts) > 0 && in.SelfHost != "" && !hostAllowed(hosts, in.SelfHost) {
		f := blocking("app_hosts", "settings.would_lock_you_out",
			map[string]string{"host": in.SelfHost})
		if in.Lockout == LockoutWarns {
			f.Level = LevelWarn
		}
		out = append(out, f)
	}

	if cidrs, cpresent, cerr := stringList(body, "trusted_proxies"); cerr == nil && cpresent {
		for _, c := range cidrs {
			p, perr := netip.ParsePrefix(strings.TrimSpace(c))
			if perr != nil {
				out = append(out, blocking("trusted_proxies", "settings.invalid_cidr",
					map[string]string{"value": c}))
				continue
			}
			// A range covering everything means any client can claim any
			// address through a forwarded header, which decides what the rate
			// limiter counts and what the audit log records.
			if p.Bits() == 0 {
				out = append(out, warning("trusted_proxies", "settings.proxy_range_is_everything",
					map[string]string{"value": c}))
			}
		}
	}
	return out
}

// hostAllowed matches a host against a list exactly as the guard does.
//
// Case-insensitive equality and nothing else. There is no wildcard form: this
// used to accept "*" and "*.example.test", which meant a list the guard would
// reject could pass the check and produce the very lockout the check exists to
// prevent.
func hostAllowed(list []string, host string) bool {
	for _, h := range list {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true
		}
	}
	return false
}

// HostOnly drops the port and lowercases, which is the form the host list is
// compared in.
func HostOnly(hostport string) string {
	h := hostport
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// checkHomes probes the directory homes would be served from.
//
// Turning homes on registers a share every account gets a folder under, so a
// root that cannot be written is a feature that appears in the interface and
// fails for the first person who opens it.
func checkHomes(in Input) []Finding {
	enabled, hasEnabled := in.Body["enabled"].(bool)
	root, _ := in.Body["root"].(string) //nolint:errcheck // absent and wrong-typed both mean "not named", handled below.
	if root == "" && hasEnabled && enabled {
		if in.DataDir == "" {
			return nil
		}
		root = filepath.Join(in.DataDir, "homes")
	}
	if root == "" {
		return nil
	}
	if !filepath.IsAbs(root) {
		return []Finding{blocking("root", "settings.path_must_be_absolute", nil)}
	}

	// Probed by writing, because "the directory exists" and "this process can
	// create folders in it" are different questions and only the second one
	// matters here.
	info, err := os.Stat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		parent := filepath.Dir(root)
		if _, perr := os.Stat(parent); perr != nil {
			return []Finding{blocking("root", "settings.dir_does_not_exist",
				map[string]string{"field": "root", "path": parent})}
		}
		if werr := probeWritable(parent); werr != nil {
			return []Finding{blocking("root", "settings.dir_not_writable",
				map[string]string{"field": "root", "path": parent, "error": werr.Error()})}
		}
		return []Finding{noted("root", "settings.dir_will_be_created",
			map[string]string{"path": root})}
	case err != nil:
		return []Finding{blocking("root", "settings.dir_not_writable",
			map[string]string{"field": "root", "path": root, "error": err.Error()})}
	case !info.IsDir():
		return []Finding{blocking("root", "settings.path_is_not_a_directory",
			map[string]string{"path": root})}
	}
	if werr := probeWritable(root); werr != nil {
		return []Finding{blocking("root", "settings.dir_not_writable",
			map[string]string{"field": "root", "path": root, "error": werr.Error()})}
	}
	return []Finding{noted("root", "settings.dir_is_writable", map[string]string{"path": root})}
}

// probeWritable creates a file and removes it. Permission bits do not answer
// this on their own: a read-only mount and an unwritable ACL both look fine
// in the mode.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".sc-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	// The close is checked and the file removed either way: a failure to
	// close is still an answer about the directory, and leaving the probe
	// file behind would be this check littering the homes root.
	cerr := f.Close()
	rerr := os.Remove(name)
	if cerr != nil {
		return cerr
	}
	return rerr
}

// checkSMB renders the configuration that would be published.
//
// Rendered with the real renderer rather than a second copy of its rules,
// which is the only way the preview and the publish cannot disagree.
func checkSMB(in Input) []Finding {
	var out []Finding
	body := in.Body
	if v, ok := body["totp_policy"].(string); ok && v != "require_separate" && v != "block" {
		out = append(out, blocking("totp_policy", "settings.unknown_totp_policy", nil))
	}
	if v, ok := body["service_gid"].(float64); ok && v == 0 {
		out = append(out, blocking("service_gid", "settings.gid_zero_is_root", nil))
	}

	cfg := smb.Config{Enabled: true, Workgroup: "WORKGROUP", ServiceUser: "scsvc"}
	if v, ok := body["workgroup"].(string); ok && v != "" {
		cfg.Workgroup = v
	}
	if v, ok := body["server_name"].(string); ok {
		cfg.ServerName = v
	}
	if v, ok := body["service_user"].(string); ok && v != "" {
		cfg.ServiceUser = v
	}
	if _, err := smb.Render(cfg, nil); err != nil {
		out = append(out, blocking("workgroup", "settings.smb_render_failed",
			map[string]string{"error": err.Error()}))
	}

	// Only worth probing when SMB is being turned on: the directory is the
	// sidecar's, and its absence is expected on a server not running one.
	if on, ok := body["enabled"].(bool); ok && on && in.SMBConfigDir != "" {
		if _, err := os.Stat(in.SMBConfigDir); err != nil {
			out = append(out, warning("enabled", "settings.smb_config_dir_unavailable",
				map[string]string{"path": in.SMBConfigDir, "error": err.Error()}))
		} else if werr := probeWritable(in.SMBConfigDir); werr != nil {
			out = append(out, warning("enabled", "settings.smb_config_dir_unavailable",
				map[string]string{"path": in.SMBConfigDir, "error": werr.Error()}))
		}
	}
	return out
}

// checkWatch compares the proposed hot set against what the kernel will
// actually give this process.
//
// A watch bound above the kernel's own limit is accepted by the interface and
// then silently not honoured, which shows up much later as changes that do not
// appear until something else triggers a rescan.
func checkWatch(body map[string]any) []Finding {
	want, ok := body["hot_set_max"].(float64)
	if !ok {
		return nil
	}
	raw, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		return nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || limit <= 0 {
		return nil
	}
	if int64(want) > int64(limit) {
		return []Finding{warning("hot_set_max", "settings.above_kernel_watch_limit",
			map[string]string{
				"value": strconv.FormatInt(int64(want), 10),
				"limit": strconv.Itoa(limit),
			})}
	}
	return []Finding{noted("hot_set_max", "settings.within_kernel_watch_limit",
		map[string]string{"limit": strconv.Itoa(limit)})}
}

// Refused turns the first blocking finding into the error a save answers with.
//
// The first rather than all of them: the error shape carries one reason, and
// the findings list is where a caller sees the rest.
func Refused(findings []Finding) error {
	for _, f := range findings {
		if f.Level != LevelBlock {
			continue
		}
		args := make([]apierr.Arg, 0, len(f.ReasonParams)+1)
		if f.Field != "" {
			args = append(args, apierr.Arg{Name: "field", Value: f.Field})
		}
		for k, v := range f.ReasonParams {
			if k == "field" && f.Field != "" {
				continue
			}
			args = append(args, apierr.Arg{Name: k, Value: v})
		}
		return &apierr.RequestError{
			Status: http.StatusUnprocessableEntity, Code: apierr.CodeInvalidRequest,
			Message: "the settings change was refused",
			Key:     apierr.MessageKey(f.ReasonKey), Args: args,
		}
	}
	return nil
}

// Sections is every section a settings document may hold.
//
// An allow-list, so a typo in a request stores nothing. Without it a client
// asking for a section that does not exist would create it, and the screen
// would then show a setting no code reads.
func Sections() []string {
	return []string{
		"network", "db", "symlink-policy", "homes", "smb",
		"search", "archive", "watch", "paths", "oidc",
		// The request rate bounds. They were absent while the settings
		// snapshot advertised them as editable, so the screen offered a
		// control whose save answered "no such section".
		"rate",
		// The sandbox policy. With no config file this is the only place it
		// can be set, and a deployment that cannot apply a layer has to be
		// able to say so without editing a file that no longer exists.
		"security",
	}
}

// Known reports whether this build recognises a section name.
func Known(section string) bool {
	for _, s := range Sections() {
		if s == section {
			return true
		}
	}
	return false
}

// Bounds is the numeric range each field may hold.
//
// One table, read by every surface that saves, so they cannot drift into
// disagreeing about what is acceptable.
func Bounds() map[string]map[string]runtimecfg.Bound {
	return map[string]map[string]runtimecfg.Bound{
		"search": {
			"max_concurrent_fast":   runtimecfg.BoundSearchConcurrent(),
			"max_concurrent_slow":   runtimecfg.BoundSearchConcurrent(),
			"walk_deadline_fast_ms": runtimecfg.BoundSearchDeadlineMs(),
			"walk_deadline_slow_ms": runtimecfg.BoundSearchDeadlineMs(),
		},
		"archive": {"max_concurrent": runtimecfg.BoundArchiveEntries()},
		"watch": {
			"hot_set_max":    runtimecfg.BoundWatchHotSet(),
			"full_threshold": runtimecfg.BoundWatchFullThreshold(),
		},
		"rate": {"per_sec": runtimecfg.BoundRatePerSec(), "burst": runtimecfg.BoundRateBurst()},
		// Zero is refused rather than read as "unset": it is root's group, the
		// agent runs as root, and an account file putting every SMB account in
		// it would be applied rather than questioned.
		"smb": {"service_gid": runtimecfg.BoundServiceGID()},
	}
}

// stringList reads one list field. Present-but-wrong is an error; absent is
// not, because a patch names the fields it changes.
func stringList(body map[string]any, key string) ([]string, bool, error) {
	raw, present := body[key]
	if !present {
		return nil, false, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false, apierr.Unprocessable("settings.must_be_at_least_one", key)
	}
	out := make([]string, 0, len(items))
	for _, v := range items {
		s, ok := v.(string)
		if !ok {
			return nil, false, apierr.Unprocessable("settings.must_be_at_least_one", key)
		}
		out = append(out, s)
	}
	return out, true, nil
}
