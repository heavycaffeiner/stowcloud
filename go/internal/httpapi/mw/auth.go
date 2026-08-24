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
			path := r.URL.Path
			if isPublic(r.Method, path) {
				next.ServeHTTP(w, r)
				return
			}
			// The WebDAV mount answers its own refusal, with the challenge a
			// client needs in order to know to send anything. So a credential
			// is still read here, and only the refusal is left to the mount:
			// treating the whole prefix as public skipped the reading too, and
			// then no credential ever arrived.
			challenges := strings.HasPrefix(path, "/dav")
			// OPTIONS never 401s: a browser strips credentials from a
			// preflight by design, and the client needs the capability
			// headers back to decide how to send the real request.
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Basic, which is the only credential a WebDAV or a sync client can
			// send. The password is an app password, never the account password:
			// a protocol that hands the real credential to every request is a
			// protocol whose credential cannot be revoked on its own.
			if _, pass, ok := r.BasicAuth(); ok {
				// The token identifies the account by itself, so the name half is
				// not read. Comparing it would mean trusting a value the same
				// request supplied, and a token that verifies already names
				// whoever it was minted for.
				principal, scope, err := svc.VerifyAppPassword(r.Context(), pass)
				if err == nil {
					ctx := withAppPWScope(withPrincipal(r.Context(), principal), scope)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				if challenges {
					next.ServeHTTP(w, r)
					return
				}
				apierr.Write(w, http.StatusUnauthorized,
					apierr.NewError(apierr.CodeAuthInvalid, "invalid credentials", ""))
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

			if challenges {
				next.ServeHTTP(w, r)
				return
			}
			// No credential was offered at all, which is a different fact from
			// one that was offered and did not verify. A client that has never
			// signed in shows a sign-in screen; one whose credential stopped
			// working shows that its session ended. Reporting both as invalid
			// tells the first one its credential was rejected when it sent none.
			apierr.Write(w, http.StatusUnauthorized,
				apierr.NewError(apierr.CodeAuthRequired, "authentication required", ""))
		})
	}
}

// PublicPaths is the predicate every mount composes: what the chain lets
// through without a credential. It is a function of method and path, and the
// one place that list lives, so a new public route is a change here and
// nowhere else.
func PublicPaths(method, path string) bool {
	switch path {
	// Signing in needs no credential by definition. Changing a password does,
	// so it is deliberately not here: it verifies the current one.
	case "/api/auth/login", "/api/auth/login/totp",
		// The config too, not only the flow: the login screen asks whether to
		// draw the button before anyone has signed in, so behind a credential
		// it is a question nobody who needs the answer can ask. It says
		// whether a provider exists and what the button reads, and withholds
		// the issuer and the client id.
		"/api/auth/oidc/config", "/api/auth/oidc/start", "/api/auth/oidc/callback",
		"/api/setup":
		return true
	case "/api/uploads":
		// The discovery request only. Everything else on this mount is a
		// credentialed operation on somebody's files.
		return method == http.MethodOptions
	case "/api/health":
		// The container runtime probes this and holds no credential, so a
		// health endpoint behind authentication is one the runtime cannot use.
		// It answers what is degraded and nothing else about the deployment.
		return method == http.MethodGet

	}
	if strings.HasPrefix(path, "/s/") {
		// Public share links: authorization is the token in the URL, never a
		// user session.
		return true
	}
	if (method == http.MethodGet || method == http.MethodHead) && !strings.HasPrefix(path, "/api/") {
		// The embedded SPA: hashed, immutable, public assets. The API is
		// exactly what a reserved /api/ prefix names, and it is not public.
		//
		// The file protocol is not under that prefix and its reads are
		// emphatically not public. Without this exclusion a read of any file
		// on the server was treated as a static asset and reached the mount
		// with no credential attached, so writing a file worked and reading
		// the same file back was refused.
		return !strings.HasPrefix(path, "/dav")
	}
	return false
}
