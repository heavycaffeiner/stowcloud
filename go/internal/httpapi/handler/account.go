package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The self-service account surface: the password, the second factor, the SMB
// toggles and the app passwords.
//
// One rule runs through all of it. Adding or replacing a durable credential
// re-confirms the account password first, because a live session alone must
// not be enough: a session is what an attacker who walked past an unlocked
// screen already has, and a credential outlives the session that created it.

// reconfirm checks the account password before a credential-changing act.
//
// A wrong password here is the credential failure it is, not a session
// failure. The distinction reaches the person: one says to try the password
// again and the other sends them back to a sign-in screen they do not need.
func reconfirm(r *http.Request, d Deps, uid int64, pw string) error {
	if pw == "" {
		return apierr.BadRequest("auth.password_required", "current_password")
	}
	ok, err := d.Auth.VerifyAccountPassword(r.Context(), uid, secret.New([]byte(pw)))
	if err != nil {
		return err
	}
	if !ok {
		return &apierr.RequestError{
			Status:  http.StatusUnauthorized,
			Code:    apierr.CodeAuthInvalid,
			Message: "the current password is not correct",
			Key:     "auth.bad_current_password",
		}
	}
	return nil
}

// ChangePassword answers POST /api/auth/password.
func ChangePassword(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			CurrentPassword     string `json:"current_password"`
			NewPassword         string `json:"new_password"`
			RevokeOtherSessions bool   `json:"revoke_other_sessions"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := reconfirm(r, d, int64(uid), req.CurrentPassword); err != nil {
			return err
		}
		if req.NewPassword == "" {
			return apierr.BadRequest("auth.password_required", "new_password")
		}
		if err := d.Auth.SetPassword(r.Context(), int64(uid), secret.New([]byte(req.NewPassword))); err != nil {
			return err
		}
		// Revoking the other sessions is the point of the checkbox: a password
		// changed because it may be known has to end the sessions that may be
		// somebody else's. This session survives, or the person is signed out
		// of the screen they just used.
		if req.RevokeOtherSessions {
			if err := revokeOtherSessions(r, d, int64(uid)); err != nil {
				return err
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// revokeOtherSessions ends every session but the one making the request.
//
// This one survives on purpose: a password changed from a screen must not sign
// the person out of the screen they changed it from, or the act looks like it
// failed.
func revokeOtherSessions(r *http.Request, d Deps, uid int64) error {
	sessions, err := d.Auth.Sessions(r.Context(), uid)
	if err != nil {
		return err
	}
	current := currentSessionHash(r)
	for _, s := range sessions {
		if current != nil && bytes.Equal(s.IDHash, current) {
			continue
		}
		if rerr := d.Auth.RevokeSessionByHash(r.Context(), uid, s.IDHash); rerr != nil {
			return rerr
		}
	}
	return nil
}

// currentSessionHash is the stored form of the token this request carried, or
// nil when it carried none. The cookie holds the token; the database holds its
// digest, which is what a comparison here has to be against.
func currentSessionHash(r *http.Request) []byte {
	c, err := r.Cookie(mw.SessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	raw, derr := hex.DecodeString(c.Value)
	if derr != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

// otpauthURL is what an authenticator app scans.
//
// The issuer is the host this server is reached under, so an app showing
// several accounts can tell them apart, and it is repeated inside the label
// because that is the form every app displays.
func otpauthURL(account, secretB32 string, hosts []string) string {
	issuer := "Stowcloud"
	if len(hosts) > 0 && hosts[0] != "" {
		issuer = hosts[0]
	}
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", issuer)
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// recoveryCodeCount is how many codes an enrolment mints. Ten is enough that
// losing the authenticator is recoverable and few enough that a printed list
// stays a list.
const recoveryCodeCount = 10

// TOTPSetup answers POST /api/auth/totp/setup: a fresh secret to enrol with.
//
// Minting it stores nothing. The secret becomes the account's only when the
// enrolment below proves the person can produce a code from it, so an
// abandoned setup leaves no half-enrolled account behind.
func TOTPSetup(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		secretB32, err := d.Auth.GenerateTOTPSecret()
		if err != nil {
			return err
		}
		name, err := d.Auth.NameOf(r.Context(), int64(uid))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]string{
			"secret":      secretB32,
			"otpauth_url": otpauthURL(name, secretB32, d.Hosts.App()),
		})
	})
}

// TOTPEnroll answers POST /api/auth/totp/enroll.
func TOTPEnroll(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Password string `json:"password"`
			Secret   string `json:"secret"`
			Code     string `json:"code"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := reconfirm(r, d, int64(uid), req.Password); err != nil {
			return err
		}
		if req.Secret == "" || req.Code == "" {
			return apierr.BadRequest("auth.totp_fields", "code")
		}
		if err := d.Auth.EnrollTOTP(r.Context(), int64(uid), req.Secret); err != nil {
			return err
		}
		// The code is checked after the secret is stored, because verifying is
		// what the stored secret is for. A code that does not verify unwinds
		// the enrolment rather than leaving an account with a factor its owner
		// cannot produce.
		ok, verr := d.Auth.VerifyTOTP(r.Context(), int64(uid), req.Code, d.Clock.Nanos())
		if verr != nil {
			return verr
		}
		if !ok {
			if derr := d.Auth.DisableTOTP(r.Context(), int64(uid)); derr != nil {
				return derr
			}
			return &apierr.RequestError{
				Status:  http.StatusUnauthorized,
				Code:    apierr.CodeAuthInvalid,
				Message: "that code does not match the secret",
				Key:     "auth.bad_totp_code",
			}
		}
		codes, gerr := d.Auth.GenerateRecoveryCodes(r.Context(), int64(uid), recoveryCodeCount)
		if gerr != nil {
			return gerr
		}
		// Returned exactly once. There is no endpoint that hands them back,
		// which is what makes them a fallback rather than a second password
		// sitting on the server.
		return writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	})
}

// TOTPDisable answers POST /api/auth/totp/disable.
func TOTPDisable(d Deps) http.HandlerFunc {
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
		if err := d.Auth.DisableTOTP(r.Context(), int64(uid)); err != nil {
			return err
		}
		// Enrolling dropped the credential derived from the account password,
		// so removing the factor mints it again from the password just
		// re-confirmed: this is the exact undo of that.
		if err := d.Auth.SetPassword(r.Context(), int64(uid), secret.New([]byte(req.Password))); err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]bool{"smb_password_replaced": true})
	})
}

// RecoveryCodes answers both halves of /api/auth/totp/recovery-codes.
//
// The count is readable without re-confirmation, because it is not a secret
// from the account's own owner. Minting a new set is, because it invalidates
// every code the person may have written down.
func RecoveryCodes(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if r.Method == http.MethodGet {
			n, err := d.Auth.RecoveryCodesRemaining(r.Context(), int64(uid))
			if err != nil {
				return err
			}
			return writeJSON(w, http.StatusOK, map[string]int{"remaining": n})
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
		codes, err := d.Auth.GenerateRecoveryCodes(r.Context(), int64(uid), recoveryCodeCount)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	})
}

// SMBSettings answers POST /api/auth/smb: the two self-service toggles.
func SMBSettings(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			OptOut  bool `json:"opt_out"`
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		// Opting out is the stronger statement and wins: it means the account
		// holds no credential for the protocol at all, which is not something
		// the other toggle can leave half-done.
		if err := d.Auth.SetSMBAccess(r.Context(), int64(uid), req.Enabled && !req.OptOut); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// SMBPassword answers POST /api/auth/smb/password.
func SMBPassword(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			CurrentPassword string `json:"current_password"`
			SMBPassword     string `json:"smb_password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if err := reconfirm(r, d, int64(uid), req.CurrentPassword); err != nil {
			return err
		}

		// Clearing it, which was mounted nowhere: the client sends this and
		// got "method not allowed" from a route that exists.
		if r.Method == http.MethodDelete {
			revertible, cerr := d.Auth.ClearSMBPassword(r.Context(), int64(uid))
			if cerr != nil {
				return cerr
			}
			// Whether the account password takes over. It does not for an
			// account carrying a second factor, linked to a provider, or
			// opted out, and saying so is the difference between "cleared"
			// and "SMB no longer works for you".
			return writeJSON(w, http.StatusOK, map[string]bool{
				"reverted_to_account_password": revertible,
			})
		}

		if req.SMBPassword == "" {
			return apierr.BadRequest("auth.password_required", "smb_password")
		}
		if err := d.Auth.SetSMBPassword(r.Context(), int64(uid), secret.New([]byte(req.SMBPassword))); err != nil {
			return err
		}
		// Setting one clears the opt-out, so the toggles the screen shows have
		// moved and it has to know.
		return writeJSON(w, http.StatusOK, map[string]bool{"smb_toggles_cleared": true})
	})
}

// AppPasswordWipe answers POST /api/auth/app-passwords/{id}/wipe.
//
// Distinct from revoking: revoking stops the credential, and this additionally
// asks the device holding it to erase its local copy of what it synced. The
// device only hears about it when it next connects, so this is a request
// rather than a guarantee and the interface says so.
func AppPasswordWipe(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("auth.bad_app_password_id", "id")
		}
		if err := d.Auth.RequestWipe(r.Context(), int64(uid), id); err != nil {
			if errors.Is(err, auth.ErrCredentials) {
				return &apierr.RequestError{
					Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
					Message: "no such app password", Key: "auth.app_password_missing",
				}
			}
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
