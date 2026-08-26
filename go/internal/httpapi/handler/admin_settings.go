// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/settingscheck"
)

// The settings sections.
//
// One handler rather than ten, because the ten differ only in which key they
// write. What they share is the part worth getting right once: a section name
// nobody recognises is refused rather than stored, and clearing a section
// removes the override so the value falls back to the file or the compiled-in
// default rather than being set to a zero.

// AdminServerSettingsSection answers PATCH and DELETE on one section.
func AdminServerSettingsSection(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		section := r.PathValue("section")
		if !settingscheck.Known(section) {
			return &apierr.RequestError{
				Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
				Message: "no such settings section", Key: "settings.unknown_section",
			}
		}

		// The body is stored as the section's document. It is decoded into a
		// map rather than a struct per section, because this layer does not
		// decide what a section means: the subsystem that reads it does, and a
		// second definition here is a second place they can disagree.
		var body map[string]any
		if err := decodeJSON(r, &body); err != nil {
			return err
		}
		// The same probes the preview runs, so the two cannot disagree about
		// what is acceptable. Anything blocking refuses the write and comes
		// back naming the field: an administrator is watching, so a value that
		// will not work is told to them rather than quietly clamped into a
		// different one or stored to fail at the next boot.
		if findings := checkSection(d, r, section, body); blocked(findings) {
			return settingsRefused(findings)
		}
		// A setting that is a credential is taken out of the document before it
		// is stored, and sealed under the master key instead. The document is
		// read whole by everything that reads any setting and is rendered to
		// this same screen, so a secret left in it would be in all of those.
		if err := extractSecrets(r, d, section, body); err != nil {
			return err
		}
		if err := d.State.MergeSettings(r.Context(), section, body); err != nil {
			return err
		}
		// Stored, then applied. The response says which of the two happened,
		// because a save that reports "applied" for a value nothing read is
		// the defect this whole surface had.
		if !reloadRuntime(r, d) {
			return writeJSON(w, http.StatusOK, applyOutcome(AppliedReserved))
		}
		return applySaved(w, r, d, section, body)
	})
}

// applySaved does whatever the saved section needs beyond the holders, and
// answers with which of the three things happened.
//
// Three, because there are three kinds of change and telling them apart is
// the point of this surface. A value the holders carry is live the moment it
// is stored. The listener's address needs a new socket, which this takes
// itself. The sandbox and what it wraps cannot be widened in a running
// process at all, so that one is a restart, and it is taken automatically
// rather than left as a button somebody has to find.
func applySaved(
	w http.ResponseWriter, r *http.Request, d Deps, section string, body map[string]any,
) error {
	// The two holders the request chain reads per request. The reload above
	// moved the values; these are the components that answer from them, and a
	// host list saved and not pushed is a guard still refusing the name that
	// was just allowed.
	if section == "network" {
		if hosts, present, err := stringList(body, "app_hosts"); err == nil && present && len(hosts) > 0 {
			d.Hosts.Set(hosts)
		}
		if cidrs, present, err := stringList(body, "trusted_proxies"); err == nil && present {
			d.Trusted.Set(runtimecfg.ParsePrefixes(cidrs))
		}
	}

	if movedBind(section, body) && d.SwapListener != nil {
		addr, _ := body["bind"].(string) //nolint:errcheck // checkSection refused anything but a string.
		if err := d.SwapListener(r.Context(), addr); err != nil {
			// The old listener is still serving: the swap binds the new socket
			// before it touches the old one. The value stays stored, so the
			// next start uses it and the screen shows the two differing.
			return &apierr.RequestError{
				Status: http.StatusUnprocessableEntity, Code: apierr.CodeInvalidRequest,
				Message: "the listener could not move to that address",
				Key:     "settings.bind_failed",
				Args: []apierr.Arg{
					{Name: "field", Value: "bind"},
					{Name: "value", Value: addr},
				},
			}
		}
		return writeJSON(w, http.StatusOK, applyOutcome(AppliedServeRestarted))
	}

	if !enginePinned(section) {
		return writeJSON(w, http.StatusOK, applyOutcome(AppliedLive))
	}

	// The engine restart. Refused while work is in flight unless the caller
	// says otherwise, because a restart during an upload loses the part nobody
	// has finished sending.
	if err := requireIdle(d, body["force"] == true); err != nil {
		// The value is stored and the process is on the old one, which the
		// screen shows as a pending row. Refusing the write instead would
		// make an administrator choose between the change and the uploads.
		return err
	}
	if d.RequestRestart == nil {
		return writeJSON(w, http.StatusOK, applyOutcome(AppliedReserved))
	}
	if err := writeJSON(w, http.StatusOK, applyOutcome(AppliedEngineRestart)); err != nil {
		return err
	}
	// After the response is written, so the caller learns the request was
	// accepted rather than losing the connection and having to guess.
	d.RequestRestart()
	return nil
}

// requireIdle refuses while an upload or a job is running, naming the counts
// so the screen can offer the forced retry.
func requireIdle(d Deps, force bool) error {
	if force || d.ActiveWork == nil {
		return nil
	}
	active := d.ActiveWork()
	if active.Uploads == 0 && active.Jobs == 0 {
		return nil
	}
	// Its own code, not the generic conflict: the screen branches on this one
	// to show the counts and offer the forced retry, and it read fs.conflict as
	// an ordinary failure with no way forward.
	return &apierr.RequestError{
		Status: http.StatusConflict, Code: apierr.CodeRestartBusy,
		Message: "work is in flight", Key: "restart.busy",
		Args: []apierr.Arg{
			{Name: "active_uploads", Value: strconv.Itoa(active.Uploads)},
			{Name: "running_jobs", Value: strconv.Itoa(active.Jobs)},
		},
	}
}

// extractSecrets moves the credential-shaped fields out of a section's
// document and into the sealed store, deleting them from the document.
//
// An absent field leaves the stored secret alone, so a save of the other OIDC
// fields does not clear the secret. An explicitly empty one clears it, which
// is what a provider that needs none looks like.
func extractSecrets(r *http.Request, d Deps, section string, body map[string]any) error {
	if section != "oidc" || d.StoreSecret == nil {
		return nil
	}
	raw, present := body["client_secret"]
	if !present {
		return nil
	}
	delete(body, "client_secret")
	v, ok := raw.(string)
	if !ok {
		return apierr.BadRequest("settings.must_be_a_string", "client_secret")
	}
	return d.StoreSecret(r.Context(), v)
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

// reloadRuntime re-reads the stored settings and pushes them into the running
// components. It reports whether the change is live.
//
// Re-read rather than applied from the patch: the stored document is what a
// restart would load, so applying it here is what makes the running server and
// the next start agree.
func reloadRuntime(r *http.Request, d Deps) bool {
	if d.Runtime == nil || d.State == nil {
		return false
	}
	d.Runtime.Set(runtimecfg.Load(r.Context(), d.State, runtimecfg.Defaults(), d.Log))
	return true
}

// What a save did. Four outcomes, because they are four different things to
// tell somebody who just pressed save.
const (
	// AppliedLive is in effect now, with nothing restarted.
	AppliedLive = "live"
	// AppliedServeRestarted is in effect now, and the listener moved to do it.
	AppliedServeRestarted = "serve_restarted"
	// AppliedEngineRestart is stored, and the process is going down to pick it
	// up. Whether it comes back is the supervisor's business.
	AppliedEngineRestart = "engine_restart"
	// AppliedReserved is stored and not in effect, because this build has
	// nothing wired to apply it. Honest rather than a success that changed
	// nothing.
	AppliedReserved = "reserved"
)

// applyOutcome is what the screen shows after a save. Saying which of the four
// happened is the point: a setting that appears saved and is not in effect is
// one an administrator will spend an afternoon on.
func applyOutcome(applied string) map[string]any {
	return map[string]any{"applied": applied}
}

// enginePinned names the sections that are decided once, when the process
// builds what is under the sandbox.
//
// The database's own file, the homes share and the SMB publisher are all
// assembled at startup. The watcher takes its bounds when it starts. The
// sandbox cannot be widened in a running process at all, and single sign-on's
// client pins its provider when it is built. Each of those is a restart, and
// this is the list of them.
//
// It has to agree with what the snapshot says per field. A section answering
// "live" while every field in it is marked restart_required is the same defect
// as the one this surface started with: a screen told the change is live when
// it is not.
func enginePinned(section string) bool {
	switch section {
	case "db", "paths", "homes", "smb", "watch", "security", "oidc":
		return true
	}
	return false
}

// movedBind reports whether this save changes the listener's address, which is
// the one field of the network section that is not already live.
func movedBind(section string, body map[string]any) bool {
	if section != "network" {
		return false
	}
	v, ok := body["bind"].(string)
	return ok && v != ""
}
