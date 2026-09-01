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
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/catalogue"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
)

// adminSettingsGet describes every settable field.
//
// The described form rather than the stored document, because the document
// alone cannot be rendered: it holds only what somebody saved, so a screen
// reading it shows nothing on a fresh deployment and has no way to know what a
// field accepts or whether changing it needs a restart.
//
// The values are the ones in force rather than the ones on disk. A stored
// value outside its bound is clamped when it loads, and showing the raw stored
// number would tell an operator the server is running on something it is not.
func (e *Engine) adminSettingsGet(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	stored, err := e.State.Settings(c.UserContext())
	if err != nil {
		return failKnown(c, err)
	}
	if stored == nil {
		// A deployment that has saved nothing has an empty document, not a
		// null one: the lookup below would otherwise have to test it first.
		stored = map[string]any{}
	}

	values := runtimecfg.Load(c.UserContext(), e.State, runtimecfg.Defaults(), e.logger)
	return writeJSON(c, fiber.StatusOK, catalogue.Of(values, stored))
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

	// The chunk bounds and the spool switch live in the upload engine's own
	// tables rather than in the settings document, so they are applied
	// through it rather than merged and reloaded. Without this the section
	// was simply unknown, and every save from the upload screen answered 422.
	if section == "upload" {
		return e.uploadSettingsPatch(c)
	}

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

	// A credential in the body never reaches the document. It is sealed under
	// the master key and stored in its own row, because the document is read
	// by anybody who may read the settings and a value that authenticates
	// this server to a provider is not something to hand them.
	if ok, written := e.extractSecrets(c, section, body); !ok {
		return written
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
	//
	// Judged by the fields this patch actually names. By section, an operator
	// changing the sign-on provider or the protocol's second-factor policy
	// was told to restart and the reload never ran, so the change sat stored
	// and inert until the container went down.
	restart := catalogue.RestartRequiredFor(section, body)
	if !restart {
		e.loadSettings(c.UserContext())
	}

	out := handler.ApplyOutcomeOf(true, !restart, restart, findings)
	if restart {
		// What a restart would interrupt, so the operator decides rather than
		// the server deciding for them. An in-flight upload loses whichever
		// part was still arriving and a running job halts where it stands;
		// both recover, but neither should happen to somebody unannounced.
		uploads, jobs := e.activeWork(c)
		out = out.WithActiveWork(uploads, jobs)
	}
	return writeJSON(c, fiber.StatusOK, out)
}

// activeWork counts what a restart would interrupt.
//
// A count that cannot be read reports one upload rather than none. Unknown
// has to read as busy: the warning is what an administrator can override,
// and guessing idle would take a restart through somebody's transfer without
// asking.
//
// No test covers that branch, and the mutation flipping it to zero is
// absorbed: producing it needs the count query to fail, which happens when
// the database does, and by then the save above it has already failed. It
// stays because the direction is the whole point, and a later caller that
// reaches this with a live database and a broken query would get the safe
// answer rather than a confident wrong one.
func (e *Engine) activeWork(c *fiber.Ctx) (uploads, jobs int) {
	w, err := e.State.CountActiveWork(c.UserContext())
	if err != nil {
		e.logger.Warn("could not count the work a restart would interrupt", "error", err)
		return 1, 0
	}
	return w.Uploads, w.Jobs
}

// extractSecrets removes credential fields from a section's body and stores
// them sealed.
//
// Removed rather than copied: leaving the value in the map would store the
// plaintext in the settings document alongside the sealed copy, which is the
// whole failure this exists to prevent.
// The bool says whether the caller may proceed and the error is the written
// response, the same shape every other gate in this package uses.
func (e *Engine) extractSecrets(c *fiber.Ctx, section string, body map[string]any) (bool, error) {
	if section != "oidc" {
		return true, nil
	}
	raw, present := body["client_secret"]
	if !present {
		// Absent means unchanged. A patch names the fields it changes, so an
		// omitted secret must not clear the stored one: an administrator
		// editing the issuer would otherwise silently break sign-in.
		return true, nil
	}
	delete(body, "client_secret")

	plain, isString := raw.(string)
	if !isString {
		// A non-string is refused rather than coerced. Storing the rendering
		// of some other type would seal a credential nobody typed.
		return false, refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if err := e.StoreConfigSecret(c.UserContext(), secretOIDCClient, plain); err != nil {
		return false, failKnown(c, err)
	}
	return true, nil
}
