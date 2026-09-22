//go:build linux && compat_nc

// The approval page of the device login.
//
// A client opens it in the system browser with nothing of the application
// behind it, so the page is rendered here rather than by the frontend bundle:
// what makes it trustworthy is all server-side, the session that says who is
// approving, the token derived from that session, and the answer for a visitor
// with neither.
package app

import (
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
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
func (e *Engine) ncLoginConsent(c *gin.Context) {
	cookie, err := c.Cookie(middleware.SessionCookieName)
	if err != nil {
		cookie = ""
	}
	_, signedIn := c.Get(string(middleware.KeyCredential))
	if cookie == "" || !signedIn {
		c.Redirect(http.StatusFound, "/login?returnTo="+url.QueryEscape(c.Request.URL.Path))
		c.Abort()
		return
	}

	nonce, err := ncConsentNonce()
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	token := c.Param("token")
	if token == "" {
		token = c.Query("token")
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	if err := ncConsentTemplate().Execute(c.Writer, map[string]string{
		"Token": token,
		"CSRF":  middleware.CSRFToken(e.csrfKey(), cookie),
		"Grant": ncGrantPath,
		"Nonce": nonce,
	}); err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Abort()
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
