// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// adminOIDCLink is the administrator's view of one account's link. Every field
// but the flag is null when there is none, rather than an empty string that
// reads as a link to nothing.
type adminOIDCLink struct {
	Linked      bool    `json:"linked"`
	Issuer      *string `json:"issuer"`
	Subject     *string `json:"subject"`
	LinkedNs    *string `json:"linked_ns"`
	LastLoginNs *string `json:"last_login_ns"`
}

// nsString renders a nanosecond stamp as a decimal string, because the value is
// past what a JSON number survives in a browser. A nil one stays null.
func nsString(ns *int64) *string {
	if ns == nil {
		return nil
	}
	s := strconv.FormatInt(*ns, 10)
	return &s
}

// Linking a single-sign-on identity to an account that already exists here.
//
// Link-only is the position, not a scope decision: the provider authenticates
// and never creates an account, so authority over who has one stays in this
// database. That is what makes revocation here total, and it is why these are
// account-management routes rather than part of signing in.

// OIDCLinkStart answers POST /api/auth/oidc/link/start.
//
// It re-confirms the account password, for the same reason enabling a second
// factor does: linking adds a way into the account that outlives this session,
// and a live session alone is what somebody at an unlocked screen already has.
func OIDCLinkStart(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Password string `json:"password"`
			ReturnTo string `json:"return_to"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := reconfirm(r, d, int64(uid), req.Password); err != nil {
			return err
		}
		if d.OIDC == nil {
			return notImplemented("oidc.not_configured")
		}

		// Deliberately no "you are already linked" check. That verdict belongs
		// to the callback, where the identity is actually known: asking here
		// would be a check against the whole round trip to the provider, and
		// the answer could change during it.
		secrets, serr := oidc.NewFlowSecrets()
		if serr != nil {
			return serr
		}
		redirectURI := oidcRedirectURI(r)

		target, aerr := d.OIDC.AuthorizeURL(r.Context(), redirectURI, secrets)
		if aerr != nil {
			d.Log.Warn("the provider could not be reached to begin a link", "error", aerr)
			return &apierr.RequestError{
				Status: http.StatusBadGateway, Code: apierr.CodeSubsystemUnavail,
				Message: "the identity provider could not be reached",
				Key:     "oidc.provider_unavailable",
			}
		}
		if ferr := d.Auth.StartOIDCFlow(r.Context(), int64(uid),
			secrets.State, secrets.Nonce, secrets.Binding, secrets.CodeVerifier,
			redirectURI, safeReturnTo(req.ReturnTo).path); ferr != nil {
			return ferr
		}

		// The binding goes to the browser rather than into the answer: it is
		// what proves the callback came back to the same browser that left,
		// and a value the page can read is one a page can be made to send.
		http.SetCookie(w, &http.Cookie{
			Name: oidcBindingCookie, Value: secrets.Binding, Path: "/api/auth/oidc",
			Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			MaxAge: oidcFlowSeconds,
		})
		// JSON, not a redirect: this call carries a password and a CSRF header,
		// so it is a fetch, and the caller navigates itself.
		return writeJSON(w, http.StatusOK, map[string]string{"authorize_url": target})
	})
}

// OIDCUnlink answers DELETE /api/auth/oidc/link.
//
// Unlinking restores local password login, which is why it re-confirms the
// password: an account whose only way in was the provider would otherwise be
// unlinked into one nobody can reach.
func OIDCUnlink(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := reconfirm(r, d, int64(uid), req.Password); err != nil {
			return err
		}
		if err := d.Auth.UnlinkOIDC(r.Context(), int64(uid)); err != nil {
			return err
		}
		// Local password login is on again, so the credential derived from
		// that password is minted again from the one just re-confirmed.
		if err := d.Auth.SetPassword(r.Context(), int64(uid), secret.New([]byte(req.Password))); err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]bool{"smb_password_replaced": true})
	})
}

// AdminUserOIDC answers the administrator's view of one account's link.
//
// An administrator can see and remove a link but never create one: creating it
// is the account owner proving they hold both credentials, and an administrator
// attaching an identity to somebody else's account is an administrator granting
// themselves that account.
func AdminUserOIDC(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("admin.bad_user_id", "id")
		}

		switch r.Method {
		case http.MethodGet:
			// The whole subject, unlike the account's own view: an
			// administrator working out why somebody cannot sign in needs the
			// exact string to compare against what the provider shows.
			link, lerr := d.Auth.OIDCLinkOf(r.Context(), id)
			if errors.Is(lerr, auth.ErrNoOIDCLink) {
				return writeJSON(w, http.StatusOK, adminOIDCLink{})
			}
			if lerr != nil {
				return lerr
			}
			out := adminOIDCLink{
				Linked:   true,
				Issuer:   &link.Issuer,
				Subject:  &link.Subject,
				LinkedNs: nsString(&link.LinkedNs),
			}
			out.LastLoginNs = nsString(link.LastLoginNs)
			return writeJSON(w, http.StatusOK, out)
		case http.MethodDelete:
			sessions, rerr := d.Auth.RevokeSessionsOf(r.Context(), id)
			if rerr != nil {
				return rerr
			}
			if err := d.Auth.RemoveOIDCLink(r.Context(), id); err != nil {
				return err
			}
			if err := d.Auth.UnlinkOIDC(r.Context(), id); err != nil {
				return err
			}
			// An administrator has no plaintext password, so the SMB
			// credential that linking removed cannot be re-derived here. The
			// answer says so rather than leaving SMB quietly broken, which is
			// why this is a document and not an empty success.
			return writeJSON(w, http.StatusOK, map[string]any{
				"smb_nt_restored":       false,
				"oidc_sessions_revoked": sessions,
			})
		}
		// Attaching an identity from here is the one thing this route does not
		// do, and refusing beats a silent success on a write nothing performs.
		return notImplemented("oidc.admin_link_refused")
	})
}
