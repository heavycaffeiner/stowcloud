//go:build linux && compat_nc

package server

import (
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"

	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
)

// The page a device login sends the browser to.
//
// It lives here rather than in the compatibility layer because everything it
// needs is the server's: the session cookie, the CSRF token derived from it,
// and the decision about what an unauthenticated visitor sees. A mount that
// minted its own would be a second answer to who a request is from.
//
// It is server-rendered rather than a route in the frontend bundle. The client
// opens this address in a system browser with no application state, and the
// only thing on the page is one button that posts a token.

// consentPage is the whole document.
//
// The two values it carries reach the script through data attributes rather
// than through the script body. html/template escapes an attribute correctly
// and cannot escape into arbitrary JavaScript, so interpolating there would
// mean marking the values as trusted, which a token out of a URL is not.
//
// A function rather than a package-level variable, so the template cannot be
// reassigned by anything that imports this package. It is parsed once, at the
// call that builds the handler.
func consentPage() *template.Template {
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
// The approval carries the CSRF header the API requires, which a plain form
// post cannot send.
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

// writeConsent renders the page, or sends an unauthenticated visitor to sign in
// first and come back.
//
// Signing in is a redirect rather than a refusal: the client opened this in a
// fresh browser, so having no session here is the ordinary case rather than an
// error, and a 401 would end the flow at the step it exists to start.
func writeConsent(state *httpapi.State) func(http.ResponseWriter, *http.Request, string) {
	page := consentPage()
	return func(w http.ResponseWriter, r *http.Request, token string) {
		c, cerr := r.Cookie(mw.SessionCookie)
		_, signedIn := mw.PrincipalFrom(r.Context())
		if cerr != nil || c.Value == "" || !signedIn {
			http.Redirect(w, r, "/login?returnTo="+url.QueryEscape(r.URL.Path), http.StatusFound)
			return
		}

		nonce, nerr := consentNonce()
		if nerr != nil {
			http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		// This page carries one inline script, and the application policy
		// admits scripts by hash. A nonce lets this document run its own
		// without widening the policy every other page is served under.
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; "+
				"connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")

		//nolint:errcheck // the headers are already written; a failed body has nowhere to go.
		page.Execute(w, struct {
			Token string
			CSRF  string
			Nonce string
		}{
			Token: token,
			CSRF:  mw.DeriveCSRFToken(state.CSRFKey, c.Value),
			Nonce: nonce,
		})
	}
}

// consentNonce is one script nonce, from the CSPRNG. A failure is reported
// rather than falling back to a fixed value: a predictable nonce is a policy
// that admits any script somebody can inject.
func consentNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b[:]), nil
}
