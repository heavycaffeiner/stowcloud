package mw

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// SessionCookie is the one cookie that carries a session. __Host- prefix
// rules out a subdomain writing it, which is what makes the cookie a session
// bound to this origin and nothing else.
const SessionCookie = "__Host-sc_sid"

// Auth is step 7. It parses the credential, session cookie first for the
// browser, Bearer app password for the programmatic clients, and records the
// principal in the context. Public paths pass through without a credential.
//
// A credential that fails validation is a 401, and it is the same 401 whether
// the token was missing, expired or forged: distinguishing them is an oracle
// an attacker can use to learn which tokens are live.
func Auth(svc *auth.Service, isPublic func(method, path string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if OriginFrom(r.Context()) == OriginContent {
				// The content origin carries no session by construction. Its
				// own capability-URL verification happens in its handler.
				next.ServeHTTP(w, r)
				return
			}
			path := r.URL.Path
			if isPublic(r.Method, path) {
				next.ServeHTTP(w, r)
				return
			}
			// OPTIONS never 401s: a browser strips credentials from a
			// preflight by design, and the client needs the capability
			// headers back to decide how to send the real request.
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Bearer app password takes priority when present.
			if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
				principal, scope, err := svc.VerifyAppPassword(r.Context(), tok)
				if err == nil {
					ctx := withAppPWScope(withPrincipal(r.Context(), principal), scope)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				apierr.Write(w, http.StatusUnauthorized,
					apierr.NewError(apierr.CodeAuthInvalid, "authentication required", ""))
				return
			}

			// Cookie session for the browser. The token travels as hex so it
			// is printable; the lookup hashes the decoded bytes, which is what
			// CreateSession hashed.
			if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
				raw, derr := hex.DecodeString(c.Value)
				if derr != nil {
					apierr.Write(w, http.StatusUnauthorized,
						apierr.NewError(apierr.CodeAuthInvalid, "authentication required", ""))
					return
				}
				principal, lerr := svc.LookupSession(r.Context(), secret.New(raw))
				if lerr == nil {
					ctx := withPrincipal(r.Context(), principal)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				apierr.Write(w, http.StatusUnauthorized,
					apierr.NewError(apierr.CodeAuthInvalid, "authentication required", ""))
				return
			}

			apierr.Write(w, http.StatusUnauthorized,
				apierr.NewError(apierr.CodeAuthInvalid, "authentication required", ""))
		})
	}
}

// PublicPaths is the predicate every mount composes: what the chain lets
// through without a credential. It is a function of method and path, and the
// one place that list lives, so a new public route is a change here and
// nowhere else.
func PublicPaths(method, path string) bool {
	switch path {
	case "/api/auth/password", "/api/auth/oidc/start", "/api/auth/oidc/callback", "/api/setup":
		return true
	case "/api/health":
		// The container runtime probes this and holds no credential, so a
		// health endpoint behind authentication is one the runtime cannot use.
		// It answers what is degraded and nothing else about the deployment.
		return method == http.MethodGet

	}
	if strings.HasPrefix(path, "/s/") || strings.HasPrefix(path, "/c/") {
		// Public share links and the content origin: authorization is the
		// token in the URL, never a user session.
		return true
	}
	if (method == http.MethodGet || method == http.MethodHead) && !strings.HasPrefix(path, "/api/") {
		// The embedded SPA: hashed, immutable, public assets. The API is
		// exactly what a reserved /api/ prefix names, and it is not public.
		return true
	}
	return false
}
