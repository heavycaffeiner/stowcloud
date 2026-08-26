// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/settingscheck"
)

// The save-time probes, which live in their own package because three
// surfaces run them: this one, the first-run form, and the emergency editor.
// The third runs in a process that may have no engine at all, so the probes
// cannot live beside the handlers that reach into one.
//
// These run on save and nowhere else. There used to be a second entry point
// and a button beside it, so an administrator pressed one thing to find out
// and another to do it; the save runs the probes anyway, so the button was
// asking a question the next click answered.

// Finding is one thing learned by probing.
type Finding = settingscheck.Finding

func blocked(findings []Finding) bool { return settingscheck.Blocked(findings) }

// checkSection probes one section's proposed values, with the surroundings
// this process can see.
func checkSection(d Deps, r *http.Request, section string, body map[string]any) []Finding {
	return settingscheck.Section(settingscheck.Input{
		Section:      section,
		Body:         body,
		SelfHost:     settingscheck.HostOnly(r.Host),
		DataDir:      d.DataDir,
		SMBConfigDir: d.SMBConfigDir,
		// The settings screen is behind the guard it is editing, so a list
		// that omits the host it was reached under refuses the save: the next
		// request, including the one that would undo it, is the one refused.
		Lockout: settingscheck.LockoutBlocks,
	})
}

// settingsRefused turns the first blocking finding into the error a save
// answers with.
func settingsRefused(findings []Finding) error { return settingscheck.Refused(findings) }
