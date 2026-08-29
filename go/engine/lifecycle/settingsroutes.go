//go:build linux

// The settings resource: one sectioned document an administrator reads whole
// and writes a section at a time.
//
// Save-time findings come from the checker rather than from probes repeated
// here. A blocking finding refuses the write; an advisory one saves and comes
// back with the answer, because an observation worth surfacing is not
// automatically an objection.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
)

// adminSettingsGet answers the whole document.
func (e *Engine) adminSettingsGet(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	doc, err := e.State.Settings(c.UserContext())
	if err != nil {
		return failKnown(c, err)
	}
	if doc == nil {
		// A deployment that has saved nothing has an empty document, not a
		// null one: a client iterating the sections would otherwise have to
		// test the field before reading it.
		doc = map[string]any{}
	}
	return writeJSON(c, fiber.StatusOK, doc)
}

// adminSettingsPatch replaces one section.
//
// One section at a time because a save is a decision about one thing. A whole
// document write would make an administrator changing a port resubmit every
// value another administrator had changed since the screen was opened.
func (e *Engine) adminSettingsPatch(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	section := c.Params("section")
	if !check.Known(section) {
		// Named rather than silently ignored: a client writing to a section
		// this build does not have has to learn that, or it reports a change
		// that never happened.
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	var body map[string]any
	if err := decodeBody(c, &body); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	findings := check.Section(check.Input{
		Section: section,
		Body:    body,

		// The host this request arrived on, so the lockout probe tests the
		// proposed list against how the administrator is actually reaching
		// the server.
		SelfHost: check.HostOnly(string(c.Request().Host())),
		DataDir:  e.dataDir,

		// Blocking, because the guard this section configures is already
		// live: a change that locked the administrator out would take hold
		// before any correction could be submitted, and the correction is
		// what would then be rejected.
		Lockout: check.LockoutBlocks,
	})

	if handler.Blocking(findings) {
		// Nothing stored and nothing applied, which the projection enforces
		// whatever is passed here.
		return writeJSON(c, fiber.StatusUnprocessableEntity,
			handler.ApplyOutcomeOf(false, false, false, findings))
	}

	if err := e.State.MergeSettings(c.UserContext(), section, body); err != nil {
		return failKnown(c, err)
	}

	// Re-read rather than applied from the patch: the stored document is what
	// a restart would load, so reloading from it is what makes the running
	// server and the next start agree.
	restart := restartRequired(section)
	if !restart {
		e.loadSettings(c.UserContext())
	}

	// The active-work counts are deliberately absent. The projection carries
	// them as optional pointers, and neither the upload engine nor the core
	// exposes a count of work in flight. Reporting zero would be a claim that
	// a restart interrupts nothing, which is the one thing an operator would
	// act on and the one thing this cannot currently know.
	return writeJSON(c, fiber.StatusOK,
		handler.ApplyOutcomeOf(true, !restart, restart, findings))
}

// restartRequired reports whether a section is decided once, when the process
// builds what sits under the sandbox.
//
// The database file, the homes share and the credential publisher are all
// assembled at startup; the watcher takes its bounds when it starts; the
// sandbox cannot be widened in a running process at all; and the sign-on
// client pins its provider when it is built. Each of those is a restart.
//
// Everything else is read per request by the chain, so reloading the document
// is enough. Reporting a section as live when it is not is the defect this
// distinction exists to prevent: an administrator then spends an afternoon on
// a setting that was never in effect.
func restartRequired(section string) bool {
	switch section {
	case "db", "paths", "homes", "smb", "watch", "security", "oidc":
		return true
	default:
		return false
	}
}
