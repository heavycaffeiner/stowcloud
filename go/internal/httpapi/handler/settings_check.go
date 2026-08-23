package handler

import (
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
)

// Checking a settings change by trying it, rather than describing it.
//
// A range in the interface says what a number may be. It does not say whether
// the directory exists, whether the renderer accepts the workgroup name, or
// whether the host list an administrator is about to save still contains the
// host they are typing into. Those are answered by looking, and looking is
// what this file does.
//
// The same probes run on save. A finding that blocks refuses the write, so the
// preview and the outcome cannot disagree: there is one set of rules and it is
// enforced once.

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

func blocking(field, key string, params map[string]string) Finding {
	return Finding{Level: "block", Field: field, ReasonKey: key, ReasonParams: params}
}

func warning(field, key string, params map[string]string) Finding {
	return Finding{Level: "warn", Field: field, ReasonKey: key, ReasonParams: params}
}

func noted(field, key string, params map[string]string) Finding {
	return Finding{Level: "ok", Field: field, ReasonKey: key, ReasonParams: params}
}

// CheckResult is what a dry run answers.
type CheckResult struct {
	Section string `json:"section"`
	// OK is false when anything blocks, which is exactly when a save of the
	// same body would be refused.
	OK              bool      `json:"ok"`
	RestartRequired bool      `json:"restart_required"`
	Findings        []Finding `json:"findings"`
}

func blocked(findings []Finding) bool {
	for _, f := range findings {
		if f.Level == "block" {
			return true
		}
	}
	return false
}

// AdminServerSettingsCheck runs the probes and stores nothing.
func AdminServerSettingsCheck(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		section := r.PathValue("section")
		if !knownSection(section) {
			return &apierr.RequestError{
				Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
				Message: "no such settings section", Key: "settings.unknown_section",
			}
		}
		var body map[string]any
		if err := decodeJSON(r, &body); err != nil {
			return err
		}
		findings := checkSection(d, r, section, body)
		return writeJSON(w, http.StatusOK, CheckResult{
			Section:         section,
			OK:              !blocked(findings),
			RestartRequired: restartRequired(section),
			Findings:        findings,
		})
	})
}

// checkSection probes one section's proposed values.
func checkSection(d Deps, r *http.Request, section string, body map[string]any) []Finding {
	var out []Finding
	// The numeric bounds first, so a value that cannot be stored is reported
	// beside the ones that can.
	for key, b := range settingsBounds()[section] {
		raw, ok := body[key]
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

	switch section {
	case "network":
		out = append(out, checkNetworkLive(r, body)...)
	case "homes":
		out = append(out, checkHomesLive(d, body)...)
	case "smb":
		out = append(out, checkSMBLive(d, body)...)
	case "watch":
		out = append(out, checkWatchLive(body)...)
	}
	if len(out) == 0 {
		out = append(out, noted("", "settings.check_passed", nil))
	}
	return out
}

// checkNetworkLive probes the host lists and the proxy ranges.
//
// The finding that earns this endpoint is the lockout: a host list that does
// not contain the host the administrator is on takes effect immediately and
// makes the next request, including the one that would undo it, answer
// "misdirected request" from a server that is otherwise healthy.
func checkNetworkLive(r *http.Request, body map[string]any) []Finding {
	var out []Finding
	for _, key := range []string{"app_hosts", "content_hosts"} {
		hosts, present, err := stringList(body, key)
		if err != nil {
			out = append(out, blocking(key, "settings.must_be_at_least_one", nil))
			continue
		}
		if !present {
			continue
		}
		if len(hosts) == 0 {
			out = append(out, blocking(key, "settings.host_list_empty", nil))
			continue
		}
		for _, h := range hosts {
			if h == "" || strings.ContainsAny(h, " /\\") {
				out = append(out, blocking(key, "settings.invalid_host", map[string]string{"value": h}))
			}
		}
	}

	// The lockout probe. Only the app host list serves this screen, so only
	// that one can strand the person saving it.
	if hosts, present, err := stringList(body, "app_hosts"); err == nil && present && len(hosts) > 0 {
		self := hostOnly(r.Host)
		if self != "" && !hostAllowed(hosts, self) {
			out = append(out, blocking("app_hosts", "settings.would_lock_you_out", map[string]string{
				"host": self,
			}))
		}
	}

	if cidrs, present, err := stringList(body, "trusted_proxies"); err == nil && present {
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

// hostOnly drops the port and lowercases, which is the form the host list is
// compared in.
func hostOnly(hostport string) string {
	h := hostport
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// checkHomesLive probes the directory homes would be served from.
//
// Turning homes on registers a share every account gets a folder under, so a
// root that cannot be written is a feature that appears in the interface and
// fails for the first person who opens it.
func checkHomesLive(d Deps, body map[string]any) []Finding {
	enabled, hasEnabled := body["enabled"].(bool)
	root, _ := body["root"].(string) //nolint:errcheck // absent and wrong-typed both mean "not named", handled below.
	if root == "" && hasEnabled && enabled {
		root = filepath.Join(d.DataDir, "homes")
		if d.DataDir == "" {
			return nil
		}
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

// checkSMBLive renders the configuration that would be published.
//
// Rendered with the real renderer rather than a second copy of its rules,
// which is the only way the preview and the publish cannot disagree.
func checkSMBLive(d Deps, body map[string]any) []Finding {
	var out []Finding
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
	if on, ok := body["enabled"].(bool); ok && on && d.SMBConfigDir != "" {
		if _, err := os.Stat(d.SMBConfigDir); err != nil {
			out = append(out, warning("enabled", "settings.smb_config_dir_unavailable",
				map[string]string{"path": d.SMBConfigDir, "error": err.Error()}))
		} else if werr := probeWritable(d.SMBConfigDir); werr != nil {
			out = append(out, warning("enabled", "settings.smb_config_dir_unavailable",
				map[string]string{"path": d.SMBConfigDir, "error": werr.Error()}))
		}
	}
	return out
}

// checkWatchLive compares the proposed hot set against what the kernel will
// actually give this process.
//
// A watch bound above the kernel's own limit is accepted by the interface and
// then silently not honoured, which shows up much later as changes that do not
// appear until something else triggers a rescan.
func checkWatchLive(body map[string]any) []Finding {
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

// settingsRefused turns the first blocking finding into the error a save
// answers with.
//
// The first rather than all of them: the error shape carries one reason, and
// the preview is where an administrator sees the whole list. A save that
// refuses is telling them something they could have seen before pressing it.
func settingsRefused(findings []Finding) error {
	for _, f := range findings {
		if f.Level != "block" {
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
