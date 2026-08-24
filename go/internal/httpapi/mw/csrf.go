package mw

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
)

// CSRF is step 8. State-changing methods on the cookie-authenticated surface
// must carry an Sc-Csrf header matching the session's derived token; Bearer
// requests are immune because a token in the Authorization header cannot be
// attached by a cross-site form.
//
// The token is stateless: hex(HMAC-SHA256(key, sha256(session_token))), so
// no database column and no per-session table are needed, and it derives from
// exactly the data the Auth step already validated.
func CSRF(key []byte, allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !stateChanging(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := AppPWScopeFrom(r.Context()); ok {
				// Bearer credentials cannot be attached cross-site, so the
				// header has nothing to prove here.
				next.ServeHTTP(w, r)
				return
			}
			// A request with no session cookie is not this layer's problem:
			// Auth refused it already, or the route is public. Checking a
			// CSRF token for nobody would 400 a public login.
			if _, ok := PrincipalFrom(r.Context()); !ok {
				next.ServeHTTP(w, r)
				return
			}
			if !originAllowed(r, allowedOrigins) {
				apierr.Write(w, http.StatusBadRequest,
					apierr.NewError(apierr.CodeInvalidRequest, "malformed request", "csrf.origin_refused"))
				return
			}
			c, err := r.Cookie(SessionCookie)
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, r)
				return
			}
			want := deriveCSRFToken(key, c.Value)
			got := r.Header.Get("Sc-Csrf")
			if got == "" || !hmac.Equal([]byte(got), []byte(want)) {
				apierr.Write(w, http.StatusBadRequest,
					apierr.NewError(apierr.CodeInvalidRequest, "malformed request", "csrf.token_mismatch"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// DeriveCSRFToken is the exported form of the derivation, for the login
// handler that has to hand the token back to a freshly minted session.
func DeriveCSRFToken(key []byte, sessionToken string) string {
	return deriveCSRFToken(key, sessionToken)
}

func deriveCSRFToken(key []byte, sessionToken string) string {
	sh := sha256.Sum256([]byte(sessionToken))
	m := hmac.New(sha256.New, key)
	m.Write(sh[:]) //nolint:errcheck // hmac.Hash.Write never fails.
	return hex.EncodeToString(m.Sum(nil))
}

func stateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// originAllowed is the referer half of CSRF. The Origin header, when the
// browser sends it, must name one of the declared origins. A request with no
// Origin header at all is refused for the state-changing methods that reach
// this point: a modern browser always sends Origin on a cross-site request,
// and the absence of one is what a form post from an old client looks like.
func originAllowed(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	// Origin is scheme://host[:port]; the declared lists carry no port.
	host := origin
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return eqAny(host, allowed)
}
