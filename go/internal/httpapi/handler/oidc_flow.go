package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
)

// Signing in through a provider, and linking one to an account.
//
// The two share every step but the last. Both redirect out, both come back to
// the same callback, and what separates them is whether the flow was started by
// somebody already signed in. That is decided from the stored flow rather than
// from the request, because the request is a redirect an attacker can deliver.
//
// The callback answers a person's browser, so it never returns JSON. It
// redirects with a symbolic code the client turns into a sentence, which is
// also what keeps this server from putting an attacker's text on a screen.

// oidcBindingCookie ties a callback to the browser that started the flow.
//
// It is what stops somebody delivering a legitimate callback URL to another
// person's browser: the state alone travels in a URL, through logs and referrer
// headers, and this does not.
const oidcBindingCookie = "sc_oidc"

// defaultReturnTo is where a flow lands when the caller named nowhere, or named
// somewhere this server will not send a browser.
const defaultReturnTo = "/"

// OIDCConfig answers GET /api/auth/oidc/config.
//
// Unauthenticated by necessity: the login screen has to know whether to draw
// the button before anyone has signed in. The issuer and the client id are
// deliberately withheld, because whether single sign-on exists is all an
// anonymous caller needs.
func OIDCConfig(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		resp := map[string]any{"enabled": false, "display_name": ""}
		if d.OIDC != nil {
			resp["enabled"] = true
			resp["display_name"] = d.OIDCDisplayName
		}
		return writeJSON(w, http.StatusOK, resp)
	})
}

// OIDCStart answers GET /api/auth/oidc/start: a redirect to the provider.
//
// A redirect rather than a document, because the browser has to actually go
// there: the provider's own sign-in page is the entire point of the flow.
func OIDCStart(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.OIDC == nil {
			return oidcFail(w, r, defaultReturnTo, "oidc.disabled")
		}
		// Login mode: no account yet. A flow started here links nothing, it
		// resolves an identity to an account that already exists.
		return oidcRedirect(w, r, d, 0, safeReturnTo(r.URL.Query().Get("returnTo")))
	})
}

// oidcRedirect mints a flow, stores it and sends the browser onward.
//
// A userID of zero is a sign-in; anything else is that account linking.
func oidcRedirect(w http.ResponseWriter, r *http.Request, d Deps, userID int64, returnTo localTarget) error {
	secrets, err := oidc.NewFlowSecrets()
	if err != nil {
		return err
	}
	redirectURI := oidcRedirectURI(r)

	target, aerr := d.OIDC.AuthorizeURL(r.Context(), redirectURI, secrets)
	if aerr != nil {
		d.Log.Warn("the provider could not be reached to begin a sign-in", "error", aerr)
		return oidcFail(w, r, returnTo.path, "oidc.provider_unavailable")
	}
	if serr := d.Auth.StartOIDCFlow(r.Context(), userID,
		secrets.State, secrets.Nonce, secrets.Binding, secrets.CodeVerifier, redirectURI, returnTo.path); serr != nil {
		return serr
	}

	http.SetCookie(w, &http.Cookie{
		Name: oidcBindingCookie, Value: secrets.Binding, Path: "/api/auth/oidc",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(oidcFlowSeconds),
	})
	// The provider's own address, which is not a local one: this is the one
	// redirect here that leaves, and it goes where discovery said rather than
	// anywhere a request named.
	http.Redirect(w, r, target, http.StatusFound)
	return nil
}

// oidcFlowSeconds is the binding cookie's life, matched to the flow's own so
// neither outlives the other.
const oidcFlowSeconds = 600

// OIDCCallback answers GET /api/auth/oidc/callback.
//
// Every failure is a redirect carrying a symbolic code, never a status a
// browser renders as an error page. The codes are deliberately coarse: an
// unknown state and a binding that does not match are one code, because the
// second is what stops a delivered callback URL and the person who receives one
// should be told to start again rather than told which check caught it.
func OIDCCallback(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.OIDC == nil {
			return oidcFail(w, r, defaultReturnTo, "oidc.disabled")
		}

		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			// The provider refused. A cancellation is its own code because it
			// is the one case where nothing went wrong.
			if e == "access_denied" {
				return oidcFail(w, r, defaultReturnTo, "oidc.access_denied")
			}
			d.Log.Info("the provider refused a sign-in", "error", e)
			return oidcFail(w, r, defaultReturnTo, "oidc.provider_unavailable")
		}

		code, state := q.Get("code"), q.Get("state")
		if code == "" || state == "" {
			return oidcFail(w, r, defaultReturnTo, "oidc.bad_request")
		}

		binding := ""
		if c, cerr := r.Cookie(oidcBindingCookie); cerr == nil {
			binding = c.Value
		}
		clearOIDCBinding(w)

		flow, ferr := d.Auth.TakeOIDCFlow(r.Context(), state, binding)
		if errors.Is(ferr, auth.ErrNoOIDCFlow) {
			return oidcFail(w, r, defaultReturnTo, "oidc.bad_state")
		}
		if ferr != nil {
			return ferr
		}

		raw, xerr := d.OIDC.Exchange(r.Context(), code, flow.RedirectURI, oidc.FlowSecrets{CodeVerifier: flow.CodeVerifier})
		if xerr != nil {
			d.Log.Warn("the provider would not exchange an authorization code", "error", xerr)
			return oidcFail(w, r, flow.ReturnTo, "oidc.provider_unavailable")
		}

		// The nonce is checked against what this flow was started with, which
		// is what ties the token to this flow rather than one an attacker
		// began. It is passed as empty and checked here because the value
		// itself is not stored, only its digest.
		claims, verr := d.OIDC.VerifyIDToken(r.Context(), raw, "")
		if verr != nil {
			d.Log.Warn("an identity token did not verify", "error", verr)
			return oidcFail(w, r, flow.ReturnTo, "oidc.bad_state")
		}
		if !auth.CheckOIDCNonce(flow, claims.Nonce) {
			return oidcFail(w, r, flow.ReturnTo, "oidc.bad_state")
		}

		if flow.User != 0 {
			return oidcCompleteLink(w, r, d, flow, claims)
		}
		return oidcCompleteLogin(w, r, d, flow, claims)
	})
}

// oidcCompleteLogin resolves a verified identity to an account here.
//
// It never creates one. That is the whole position: the provider authenticates
// and authority over who has an account stays in this database, which is what
// makes a revocation here total.
func oidcCompleteLogin(w http.ResponseWriter, r *http.Request, d Deps, flow auth.OIDCFlow, claims *oidc.Claims) error {
	userID, err := d.Auth.UserForOIDCIdentity(r.Context(), claims.Issuer, claims.Subject)
	if errors.Is(err, auth.ErrNoOIDCLink) {
		// One code for "no account has this identity" and "the account is
		// disabled", the same account-enumeration defence as everywhere else
		// here. The audit log is where the two are told apart.
		d.Log.Info("a verified identity matched no account", "issuer", claims.Issuer)
		return oidcFail(w, r, flow.ReturnTo, "oidc.not_linked")
	}
	if err != nil {
		return err
	}

	// A provider sign-in is one factor as far as this server is concerned:
	// what the provider asked for is the provider's business, and claiming a
	// second factor happened here would satisfy a policy nothing enforced.
	sess, serr := d.Auth.CreateSession(r.Context(), userID,
		mw.ClientFrom(r.Context()).String(), r.UserAgent(), amrProvider, 0)
	if serr != nil {
		return serr
	}
	if terr := d.Auth.TouchOIDCLink(r.Context(), claims.Issuer, claims.Subject); terr != nil {
		// Bookkeeping. The sign-in already succeeded, and refusing it for a
		// column that would not update is a refusal nobody can act on.
		d.Log.Warn("a sign-in's last-used stamp was not recorded", "error", terr)
	}

	setSessionCookie(w, sess)
	redirectLocal(w, r, safeReturnTo(flow.ReturnTo), "")
	return nil
}

// oidcCompleteLink attaches a verified identity to the account that started the
// flow.
//
// The account is the one recorded when the flow began, never the one presenting
// now: a session that changed in between is a different person finishing
// somebody else's link.
func oidcCompleteLink(w http.ResponseWriter, r *http.Request, d Deps, flow auth.OIDCFlow, claims *oidc.Claims) error {
	uid, cerr := userOf(r)
	if cerr != nil || int64(uid) != flow.User {
		return oidcFail(w, r, flow.ReturnTo, "oidc.link_session_changed")
	}

	err := d.Auth.CreateOIDCLink(r.Context(), flow.User, claims.Issuer, claims.Subject)
	switch {
	case errors.Is(err, auth.ErrOIDCLinkTaken):
		return oidcFail(w, r, flow.ReturnTo, "oidc.subject_already_linked")
	case err != nil:
		return err
	}

	redirectLocal(w, r, safeReturnTo(flow.ReturnTo), "")
	return nil
}

// oidcFail sends the browser back with a symbolic code.
//
// Symbolic rather than prose: the value is reflected off a redirect anybody can
// cause, so a server that sent wording would be a way to put an attacker's text
// on somebody's screen. The client holds the sentences.
func oidcFail(w http.ResponseWriter, r *http.Request, returnTo, code string) error {
	clearOIDCBinding(w)
	redirectLocal(w, r, safeReturnTo(returnTo), "oidc_error="+url.QueryEscape(code))
	return nil
}

func clearOIDCBinding(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcBindingCookie, Value: "", Path: "/api/auth/oidc",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// localTarget is a redirect destination that has been checked. The type is the
// check: nothing else in this package constructs one, so a value of this type
// cannot be a URL pointing somewhere else.
type localTarget struct{ path string }

// safeReturnTo keeps a redirect inside this server.
//
// A path, never a URL: anything carrying a scheme or a host is somewhere else,
// and this server sending a browser there on request is an open redirect. A
// leading double slash counts as a host, which is the form that gets missed.
//
// Every byte has to be printable ASCII, because the value ends up in a header
// and a control character there splits the response.
func safeReturnTo(v string) localTarget {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return localTarget{defaultReturnTo}
	}
	for i := range len(v) {
		if v[i] < 0x20 || v[i] > 0x7e {
			return localTarget{defaultReturnTo}
		}
	}
	return localTarget{v}
}

// redirectLocal is the only redirect this package performs, and it takes a
// destination that has already been through the check above.
func redirectLocal(w http.ResponseWriter, r *http.Request, to localTarget, query string) {
	target := to.path
	if query != "" {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + query
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// amrProvider records that a session was established by a provider rather than
// by a password here.
const amrProvider = 1

// oidcRedirectURI is where the provider sends the browser back.
//
// Built from the host the request arrived on, because a deployment reached
// under several names registers several of them and which one applies is
// decided per request. The same string has to reach the exchange byte for byte.
func oidcRedirectURI(r *http.Request) string {
	return "https://" + r.Host + "/api/auth/oidc/callback"
}
