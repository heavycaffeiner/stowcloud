package handler

import (
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

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
		return notImplemented("oidc.link_unavailable")
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
			return notImplemented("oidc.link_unavailable")
		case http.MethodDelete:
			if err := d.Auth.UnlinkOIDC(r.Context(), id); err != nil {
				return err
			}
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
		// Attaching an identity from here is the one thing this route does not
		// do, and refusing beats a silent success on a write nothing performs.
		return notImplemented("oidc.admin_link_refused")
	})
}
