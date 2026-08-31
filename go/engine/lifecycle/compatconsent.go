//go:build linux && compat_nc

// The approval page of the device login.
//
// A client opens it in the system browser with nothing of the application
// behind it, so the page is rendered here and carries exactly one control: a
// button that posts the token. What the page needs to be trustworthy is all
// server-side, the session that says who is approving, the token derived from
// that session, and the answer for a visitor with neither, which is why it is
// not a route the frontend bundle serves.
package lifecycle

import (
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"net/url"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
)

// consentLoginPage is the whole document.
//
// The token and the CSRF value travel to the script as data attributes and
// not as interpolated JavaScript. The template engine's escaping guarantees
// stop at a script body's boundary, so putting either there would mean
// vouching for characters that came out of a URL.
func consentLoginPage() *template.Template {
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
</style>
</head>
<body>
<main id="sc-main" data-token="{{.Token}}" data-csrf="{{.CSRF}}">
<h1>Connect a device</h1>
<p>An app on your device asked to connect to this account. Approving gives it
its own password, which you can revoke later without changing your own.</p>
<p>If you did not just start this yourself, close this page.</p>
<form id="sc-form">
  <button type="submit">Approve</button>
</form>
</main>
<script nonce="{{.Nonce}}">
document.getElementById('sc-form').addEventListener('submit', async function (e) {
  e.preventDefault();
  var main = document.getElementById('sc-main');
  var button = e.target.querySelector('button');
  button.disabled = true;
  var res = await fetch('/index.php/login/v2/grant', {
    method: 'POST',
    headers: {
      'Sc-Csrf': main.dataset.csrf,
      'Content-Type': 'application/x-www-form-urlencoded'
    },
    body: new URLSearchParams({ token: main.dataset.token }),
    credentials: 'same-origin'
  });
  main.textContent = res.ok
    ? 'Approved. You can close this page and return to the app.'
    : 'That did not work. The request may have expired; start again from the app.';
});
</script>
</body>
</html>
`))
}

// compatLoginConsent shows the page, or sends a visitor with no session to
// sign in and come back.
//
// Redirecting is the honest answer rather than a refusal: the browser that
// opened this belongs to somebody who has not signed in yet, that is the
// normal path, and a 401 here would break the flow at the one step it exists
// to start.
func (e *Engine) compatLoginConsent(c *fiber.Ctx) error {
	cookie := c.Cookies(middleware.SessionCookieName)
	_, signedIn := c.Locals(middleware.KeyCredential).(middleware.Principal)
	if cookie == "" || !signedIn {
		return c.Redirect("/login?returnTo="+consentEscapePath(c.Path()), fiber.StatusFound)
	}

	nonce, nerr := consentNonce()
	if nerr != nil {
		// A predictable nonce would be a policy admitting any script somebody
		// can inject, so a failure to mint one fails the page rather than
		// falling back.
		return fiber.NewError(fiber.StatusInternalServerError, "the page could not be rendered")
	}

	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-store")
	// This page carries one inline script, and the application policy admits
	// scripts by hash. A nonce lets this document run its own without
	// widening the policy every other page is served under.
	c.Set(fiber.HeaderContentSecurityPolicy,
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; "+
			"connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")

	return consentLoginPage().Execute(c.Response().BodyWriter(), map[string]string{
		"Token": c.Params("token"),
		// Derived from the cookie the request presented, which is the same
		// input the chain's own CSRF check verifies against. A token derived
		// from anything else would be one the grant route refuses.
		"CSRF":  middleware.CSRFToken(e.csrfKey(), cookie),
		"Nonce": nonce,
	})
}

// consentNonce is one script nonce, from the CSPRNG.
func consentNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b[:]), nil
}

// consentEscapePath makes the path safe inside the redirect's query value.
//
// A query parameter needs one escaping, and this is it: a hand-rolled
// replacement would catch the character this page happens to care about and
// miss the ampersand that ends the value.
func consentEscapePath(p string) string {
	return url.QueryEscape(p)
}
