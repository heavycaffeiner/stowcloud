// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The second step of signing in.
//
// The password screen and the code screen are two requests, so the server has
// to carry the fact that the password was already accepted from one to the
// other. It carries it in a challenge handed to the client rather than in a
// stored row, which is what keeps an abandoned sign-in from leaving anything
// behind to clean up or to steal.
//
// The challenge is signed and short-lived. It names the account and the moment
// it was minted, and the signature is what makes it unforgeable: without one, a
// client could present a challenge for any account and skip the password
// entirely, which is the whole of the first step.

// challengeTTL is how long the code screen may take. Long enough to open an
// authenticator app and read a number, short enough that a challenge left on a
// screen is not a standing credential.
const challengeTTL = 5 * 60

// mintChallenge signs the fact that this account's password was accepted.
func mintChallenge(key []byte, userID int64, nowUnix int64) string {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		// A challenge without randomness would be replayable within its
		// window, so failing here is better than issuing one.
		return ""
	}
	body := strconv.FormatInt(userID, 10) + ":" +
		strconv.FormatInt(nowUnix, 10) + ":" + hex.EncodeToString(nonce)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body)) //nolint:errcheck // hash.Write never fails.
	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// openChallenge verifies one and returns the account it names.
func openChallenge(key []byte, challenge string, nowUnix int64) (int64, bool) {
	if len(challenge) > limits.OIDCTokenBytes {
		return 0, false
	}
	encBody, encMAC, ok := strings.Cut(challenge, ".")
	if !ok {
		return 0, false
	}
	body, err := base64.RawURLEncoding.DecodeString(encBody)
	if err != nil {
		return 0, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(encMAC)
	if err != nil {
		return 0, false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body) //nolint:errcheck // hash.Write never fails.
	// Compared without an early exit, because the comparison decides whether
	// somebody who did not present a password gets in.
	if !hmac.Equal(mac.Sum(nil), sig) {
		return 0, false
	}

	parts := strings.Split(string(body), ":")
	if len(parts) != 3 {
		return 0, false
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	issued, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	if nowUnix-issued > challengeTTL || issued > nowUnix {
		return 0, false
	}
	return userID, true
}

// LoginTOTP answers POST /api/auth/login/totp: the code screen.
func LoginTOTP(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Challenge string `json:"challenge"`
			Code      string `json:"code"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Challenge == "" || req.Code == "" {
			return apierr.BadRequest("auth.login_fields", "code")
		}

		uid, ok := openChallenge(d.CSRFKey, req.Challenge, d.Clock.Now().Unix())
		if !ok {
			// An expired challenge and a forged one answer identically. The
			// person starts again either way, and telling them apart tells a
			// forger which half to work on.
			return &apierr.RequestError{
				Status: http.StatusUnauthorized, Code: apierr.CodeAuthInvalid,
				Message: "start signing in again", Key: "auth.challenge_expired",
			}
		}

		accepted, err := d.Auth.VerifyTOTP(r.Context(), uid, req.Code, d.Clock.Nanos())
		if err != nil {
			return err
		}
		if !accepted {
			// A recovery code is the other thing a person can type here, and
			// it is checked second because it is the one that gets consumed.
			used, uerr := d.Auth.UseRecoveryCode(r.Context(), uid, req.Code)
			if uerr != nil {
				return uerr
			}
			if !used {
				return &apierr.RequestError{
					Status: http.StatusUnauthorized, Code: apierr.CodeAuthInvalid,
					Message: "that code is not right", Key: "auth.bad_totp_code",
				}
			}
		}

		name, nerr := d.Auth.NameOf(r.Context(), uid)
		if nerr != nil {
			return nerr
		}
		sess, serr := d.Auth.CreateSession(r.Context(), uid,
			mw.ClientFrom(r.Context()).String(), r.UserAgent(), amrPasswordAndFactor, 0)
		if serr != nil {
			return serr
		}

		resp := loginResponse{Status: "ok"}
		resp.User.ID = uid
		resp.User.Name = name
		setSessionCookie(w, sess)
		return writeJSON(w, http.StatusOK, resp)
	})
}

// amrPasswordAndFactor records that this session was established with both a
// password and a second factor, which is what a policy asking for one reads.
const amrPasswordAndFactor = 2

// secondFactorChallenge turns the refusal from the password step into the
// challenge the code screen presents.
func secondFactorChallenge(d Deps, r *http.Request, name string, err error) (loginChallenge, bool) {
	if !errors.Is(err, auth.ErrSecondFactor) {
		return loginChallenge{}, false
	}
	// The account is looked up again rather than carried out of the failed
	// call, because the call deliberately returns nothing about who it was
	// refusing.
	uid, lerr := d.Auth.UserIDByName(r.Context(), name)
	if lerr != nil {
		return loginChallenge{}, false
	}
	c := mintChallenge(d.CSRFKey, uid, d.Clock.Now().Unix())
	if c == "" {
		return loginChallenge{}, false
	}
	return loginChallenge{Status: "totp_required", Challenge: c}, true
}

// loginChallenge is what the password step answers for an account with a
// second factor.
type loginChallenge struct {
	Status    string `json:"status"`
	Challenge string `json:"challenge"`
}

// setSessionCookie is the one place the session cookie's attributes are set,
// so the two sign-in paths cannot disagree about them.
func setSessionCookie(w http.ResponseWriter, sess auth.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    hex.EncodeToString(sess.Token.Reveal()),
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
