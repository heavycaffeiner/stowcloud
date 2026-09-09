//go:build linux && compat_nc

// The approval page of the device login.
//
// A client opens it in the system browser with nothing of the application
// behind it, so the page is rendered here rather than by the frontend bundle:
// what makes it trustworthy is all server-side, the session that says who is
// approving, the token derived from that session, and the answer for a visitor
// with neither.
package lifecycle

import (
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"net/url"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
)

// ncConsentTemplate is the whole document.
//
// The token and the CSRF value travel to the script as data attributes rather
// than as interpolated JavaScript. The template engine's escaping guarantees
// stop at a script body's boundary, so putting either there would mean
// vouching for characters that arrived in a URL.
func ncConsentTemplate() *template.Template {
	return template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>Connect a device</title>
<style>
:root { color-scheme: light dark; }
body {
  font: 16px/1.5 system-ui, sans-serif;
  margin: 0; min-height: 100vh;
  display: grid; place-items: center; padding: 1.5rem;
}
main { max-width: 26rem; width: 100%; }
h1 { font-size: 1.25rem; margin: 0 0 .75rem; }
p { margin: 0 0 1rem; }
button {
  font: inherit; padding: .6rem 1.1rem; border-radius: .4rem;
  border: 0; cursor: pointer; color: white; background: #2563eb;
}
button:disabled { opacity: .6; cursor: default; }
#sc-status { margin-top: 1rem; color: #b91c1c; }
</style>
</head>
<body>
<main id="sc-main" data-token="{{.Token}}" data-csrf="{{.CSRF}}" data-grant="{{.Grant}}">
<h1>Connect a device</h1>
<p>An app on your device asked to connect to this account. Approving gives it
its own password, which you can revoke later without changing yours.</p>
<p>If you did not just start this yourself, close this page.</p>
<p id="sc-status" hidden></p>
<button id="sc-approve" type="button">Approve</button>
</main>
<script nonce="{{.Nonce}}">
document.getElementById('sc-approve').addEventListener('click', async function () {
  var main = document.getElementById('sc-main');
  var button = document.getElementById('sc-approve');
  var status = document.getElementById('sc-status');
  button.disabled = true;
  status.hidden = true;

  function fail(message) {
    button.disabled = false;
    status.textContent = message;
    status.hidden = false;
  }

  try {
    var res = await fetch(main.dataset.grant, {
      method: 'POST',
      headers: {
        'Sc-Csrf': main.dataset.csrf,
        'Content-Type': 'application/x-www-form-urlencoded'
      },
      body: 'token=' + encodeURIComponent(main.dataset.token),
      credentials: 'same-origin'
    });
    if (res.ok) {
      main.textContent = 'Approved. You can close this page and return to the app.';
      return;
    }
    if (res.status === 401) {
      window.location.href = '/login?returnTo=' + encodeURIComponent(window.location.pathname);
      return;
    }
    fail('That did not work (' + res.status + '). The request may have expired; start again from the app.');
  } catch (err) {
    fail('Network error. Please try again.');
  }
});
</script>
</body>
</html>
`))
}

// ncLoginConsent shows the page, or sends a visitor with no session to sign in
// and come back.
//
// Redirecting is the honest answer rather than a refusal: the browser that
// opened this belongs to somebody who has not signed in yet, that is the
// normal path, and a 401 here breaks the flow at the one step it exists to
// start.
func (e *Engine) ncLoginConsent(c *fiber.Ctx) error {
	cookie := c.Cookies(middleware.SessionCookieName)
	_, signedIn := c.Locals(middleware.KeyCredential).(middleware.Principal)
	if cookie == "" || !signedIn {
		return c.Redirect("/login?returnTo="+url.QueryEscape(c.Path()), fiber.StatusFound)
	}

	nonce, err := ncConsentNonce()
	if err != nil {
		// A predictable nonce is a policy admitting any script somebody can
		// inject, so a failure to mint one fails the page rather than falling
		// back to a weaker one.
		return fiber.NewError(fiber.StatusInternalServerError, "the page could not be rendered")
	}

	token := c.Params("token")
	if token == "" {
		token = c.Query("token")
	}

	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-store")
	// This page carries one inline script, and the application's policy admits
	// scripts by hash. A nonce lets this document run its own without widening
	// the policy every other page is served under.
	c.Set(fiber.HeaderContentSecurityPolicy,
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; "+
			"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")

	return ncConsentTemplate().Execute(c.Response().BodyWriter(), map[string]string{
		"Token": token,
		// Derived from the cookie this request presented, which is the same
		// input the chain's own check verifies against. A value derived from
		// anything else would be one the grant route refuses.
		"CSRF": middleware.CSRFToken(e.csrfKey(), cookie),
		// The grant endpoint, spelled by the surface that owns it rather than
		// reconstructed in the browser from the current path.
		"Grant": ncGrantPath,
		"Nonce": nonce,
	})
}

// ncGrantPath is where the page posts the approval.
const ncGrantPath = "/index.php/login/v2/grant"

// ncConsentNonce is one script nonce, from the system random source.
func ncConsentNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(raw[:]), nil
}
