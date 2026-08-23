package handler

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
)

// The settings sections.
//
// One handler rather than ten, because the ten differ only in which key they
// write. What they share is the part worth getting right once: a section name
// nobody recognises is refused rather than stored, and clearing a section
// removes the override so the value falls back to the file or the compiled-in
// default rather than being set to a zero.

// settingsSections is every section this build recognises.
//
// An allow-list, so a typo in a request stores nothing. Without it a client
// asking for a section that does not exist would create it, and the screen
// would then show a setting no code reads.
func settingsSections() []string {
	return []string{
		"network", "db", "symlink-policy", "homes", "smb",
		"search", "archive", "watch", "paths", "oidc",
		// The request rate bounds. They were absent while the settings
		// snapshot advertised them as editable, so the screen offered a
		// control whose save answered "no such section".
		"rate",
	}
}

// AdminServerSettingsSection answers PATCH and DELETE on one section.
func AdminServerSettingsSection(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		section := r.PathValue("section")
		if !slices.Contains(settingsSections(), section) {
			return &apierr.RequestError{
				Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
				Message: "no such settings section", Key: "settings.unknown_section",
			}
		}

		if r.Method == http.MethodDelete {
			if err := d.State.ClearSettings(r.Context(), section); err != nil {
				return err
			}
			return writeJSON(w, http.StatusOK, applyOutcome(section, false))
		}

		// The body is stored as the section's document. It is decoded into a
		// map rather than a struct per section, because this layer does not
		// decide what a section means: the subsystem that reads it does, and a
		// second definition here is a second place they can disagree.
		var body map[string]any
		if err := decodeJSON(r, &body); err != nil {
			return err
		}
		// Validated before it is stored, and refused naming the field. An
		// administrator is watching, so a value outside its bound is told to
		// them rather than quietly clamped into a different one.
		if verr := checkSection(section, body); verr != nil {
			return verr
		}
		if err := d.State.MergeSettings(r.Context(), section, body); err != nil {
			return err
		}
		// Stored, then applied. The response says which of the two happened,
		// because a save that reports "applied" for a value nothing read is
		// the defect this whole surface had.
		//
		// A section this process cannot move while running says so whatever
		// the reload did: the listener's address and the database's own file
		// are fixed when they are opened.
		needsRestart := restartRequired(section) || !reloadRuntime(r, d)
		return writeJSON(w, http.StatusOK, applyOutcome(section, needsRestart))
	})
}

// checkSection validates the fields this build knows how to move.
//
// A key it does not recognise is stored without a check rather than refused:
// the sections belong to the subsystems that read them, and refusing an
// unknown key here would make this layer the authority on what a section may
// contain, which is the second definition the comment above warns about.
func checkSection(section string, body map[string]any) error {
	bounds := map[string]map[string]runtimecfg.Bound{
		"search": {
			"max_concurrent_fast":   runtimecfg.BoundSearchConcurrent(),
			"max_concurrent_slow":   runtimecfg.BoundSearchConcurrent(),
			"walk_deadline_fast_ms": runtimecfg.BoundSearchDeadlineMs(),
			"walk_deadline_slow_ms": runtimecfg.BoundSearchDeadlineMs(),
		},
		"archive": {"max_concurrent": runtimecfg.BoundArchiveEntries()},
		"watch":   {"hot_set_max": runtimecfg.BoundWatchHotSet()},
		"rate":    {"per_sec": runtimecfg.BoundRatePerSec(), "burst": runtimecfg.BoundRateBurst()},
	}
	for key, b := range bounds[section] {
		raw, ok := body[key]
		if !ok {
			continue
		}
		n, ok := raw.(float64)
		if !ok {
			return apierr.Unprocessable("settings.must_be_at_least_one", key)
		}
		if err := runtimecfg.Check(key, int64(n), b); err != nil {
			return apierr.Unprocessable("settings.must_be_at_least_one", key)
		}
	}
	return nil
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
	d.Runtime.Set(runtimecfg.Load(r.Context(), d.State, d.Runtime.Base(), d.Log))
	return true
}

// applyOutcome is what the screen shows after a save: whether the value is
// live now or waiting for a restart. Saying so is the point, because a setting
// that appears saved and is not in effect is one an administrator will spend
// an afternoon on.
func applyOutcome(section string, needsRestart bool) map[string]any {
	return map[string]any{
		"section":          section,
		"applied":          !needsRestart,
		"restart_required": needsRestart,
	}
}

// restartRequired names the sections this process cannot move while running.
//
// The listener's address and the database's own file are fixed when they are
// opened, and pretending otherwise would report a change that did not happen.
func restartRequired(section string) bool {
	switch section {
	case "db", "paths", "homes":
		return true
	}
	return false
}

// AdminServerSettingsRestart answers POST /api/admin/server-settings/restart.
//
// The process exits and the supervisor starts it again. It is a restart
// request rather than a restart: nothing here can bring the process back, and
// a deployment with no supervisor stays stopped, which is why the exit code
// says a restart was asked for rather than that something failed.
func AdminServerSettingsRestart(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		var req struct {
			Force bool `json:"force"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		// Work in flight is a reason to refuse, because a restart during an
		// upload loses the part nobody has finished sending. The force flag is
		// the administrator saying they know.
		if !req.Force && d.ActiveWork != nil {
			active := d.ActiveWork()
			if active.Uploads > 0 || active.Jobs > 0 {
				return &apierr.RequestError{
					Status: http.StatusConflict, Code: apierr.CodeFsConflict,
					Message: "work is in flight", Key: "restart.busy",
					Args: []apierr.Arg{
						{Name: "active_uploads", Value: strconv.Itoa(active.Uploads)},
						{Name: "running_jobs", Value: strconv.Itoa(active.Jobs)},
					},
				}
			}
		}

		// The response is written before the exit is scheduled, so the caller
		// learns the request was accepted rather than losing the connection
		// and having to guess.
		if err := writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"}); err != nil {
			return err
		}
		if d.RequestRestart != nil {
			d.RequestRestart()
		}
		return nil
	})
}
